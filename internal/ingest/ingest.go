// Package ingest turns a source repository into a Waqwaq wiki: one markdown page
// per package, linked by the package's real dependency edges as [[wikilinks]].
// It is deterministic; the structure comes from the code, not an LLM.
package ingest

import (
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Go ingests the Go module rooted at repoDir, writing one page per first-party
// package under outDir/wiki, plus an index. It returns the number of pages.
func Go(repoDir, outDir string) (int, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedModule | packages.NeedSyntax,
		Dir:  repoDir,
		ParseFile: func(fset *token.FileSet, filename string, src []byte) (*ast.File, error) {
			return parser.ParseFile(fset, filename, src, parser.ParseComments)
		},
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return 0, err
	}
	if len(pkgs) == 0 {
		return 0, fmt.Errorf("no Go packages found in %s", repoDir)
	}

	modPath := ""
	for _, p := range pkgs {
		if p.Module != nil && p.Module.Path != "" {
			modPath = p.Module.Path
			break
		}
	}
	if modPath == "" {
		return 0, fmt.Errorf("no Go module found in %s (need a go.mod)", repoDir)
	}

	first := make(map[string]*packages.Package, len(pkgs))
	for _, p := range pkgs {
		first[p.PkgPath] = p
	}

	wikiDir := filepath.Join(outDir, "wiki")
	if err := os.MkdirAll(wikiDir, 0o755); err != nil {
		return 0, err
	}

	slug := func(pkgPath string) string {
		rel := strings.TrimPrefix(strings.TrimPrefix(pkgPath, modPath), "/")
		if rel == "" {
			return "main"
		}
		return rel
	}

	count := 0
	var slugs []string
	for path, p := range first {
		s := slug(path)
		dst := filepath.Join(wikiDir, filepath.FromSlash(s)+".md")
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return count, err
		}
		if err := os.WriteFile(dst, []byte(renderPkg(p, modPath, first, slug)), 0o644); err != nil {
			return count, err
		}
		slugs = append(slugs, s)
		count++
	}

	if err := writeIndex(wikiDir, modPath, slugs); err != nil {
		return count, err
	}
	return count, nil
}

func renderPkg(p *packages.Package, modPath string, first map[string]*packages.Package, slug func(string) string) string {
	var b strings.Builder
	rel := strings.TrimPrefix(strings.TrimPrefix(p.PkgPath, modPath), "/")
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", p.Name)
	if rel == "" {
		b.WriteString("tags: [package, root]\n")
	} else {
		b.WriteString("tags: [package]\n")
	}
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "Import path: `%s`\n\n", p.PkgPath)

	dp := docPackage(p)
	if dp != nil && strings.TrimSpace(dp.Doc) != "" {
		b.WriteString(sanitizeDoc(strings.TrimSpace(dp.Doc)) + "\n\n")
	}

	var imps []string
	for ip := range p.Imports {
		if _, ok := first[ip]; ok && ip != p.PkgPath {
			imps = append(imps, ip)
		}
	}
	sort.Strings(imps)
	if len(imps) > 0 {
		b.WriteString("## Imports\n\n")
		for _, ip := range imps {
			fmt.Fprintf(&b, "- [[%s|%s]]\n", slug(ip), first[ip].Name)
		}
		b.WriteString("\n")
	}

	if dp != nil && (len(dp.Types) > 0 || len(dp.Funcs) > 0) {
		b.WriteString("## API\n\n")
		for _, t := range dp.Types {
			fmt.Fprintf(&b, "- **type %s** · %s\n", t.Name, oneline(dp, t.Doc))
		}
		for _, f := range dp.Funcs {
			fmt.Fprintf(&b, "- **func %s** · %s\n", f.Name, oneline(dp, f.Doc))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func docPackage(p *packages.Package) *doc.Package {
	if len(p.Syntax) == 0 || p.Fset == nil {
		return nil
	}
	dp, err := doc.NewFromFiles(p.Fset, p.Syntax, p.PkgPath)
	if err != nil {
		return nil
	}
	return dp
}

func oneline(dp *doc.Package, s string) string {
	if syn := dp.Synopsis(s); syn != "" {
		return sanitizeDoc(syn)
	}
	return "(undocumented)"
}

// sanitizeDoc neutralises [[ ]] in Go doc text so prose does not become a wikilink.
func sanitizeDoc(s string) string {
	return strings.NewReplacer("[[", "[", "]]", "]").Replace(s)
}

func writeIndex(wikiDir, modPath string, slugs []string) error {
	sort.Strings(slugs)
	var b strings.Builder
	b.WriteString("---\ntitle: " + modPath + "\ntags: [overview]\n---\n\n")
	fmt.Fprintf(&b, "Architecture of `%s`, generated from the Go import graph. Every page is a package; every link is a real import.\n\n", modPath)
	b.WriteString("## Packages\n\n")
	for _, s := range slugs {
		fmt.Fprintf(&b, "- [[%s]]\n", s)
	}
	b.WriteString("\n")
	return os.WriteFile(filepath.Join(wikiDir, "index.md"), []byte(b.String()), 0o644)
}
