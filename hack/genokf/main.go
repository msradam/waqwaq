// Command genokf writes a large, realistic OKF wiki for load and scale testing.
// It is a development tool, not part of the read-only waqwaq binary.
//
// Usage:
//
//	go run ./hack/genokf -out /tmp/bigwiki -pages 100000
//
// The output is a valid OKF bundle: typed concept pages (datasets, tables,
// metrics, references) with descriptions, tags, timestamps, external resource
// URLs, cross-links by relative .md path, and index.md directory listings.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	out := flag.String("out", "okf-wiki", "output directory")
	pages := flag.Int("pages", 10000, "number of concept pages to generate")
	flag.Parse()

	if err := gen(*out, *pages); err != nil {
		fmt.Fprintln(os.Stderr, "genokf:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d concept pages to %s\n", *pages, *out)
}

// types and their directories mirror how real Google OKF bundles are laid out.
var kinds = []struct{ typ, dir string }{
	{"BigQuery Table", "tables"},
	{"BigQuery Dataset", "datasets"},
	{"Metric", "references/metrics"},
	{"Reference", "references"},
}

// relLink returns the relative .md link from fromSlug to page j, correct across
// the mixed directory depths (tables/ vs references/metrics/).
func relLink(fromSlug string, j int) string {
	toSlug := kinds[j%len(kinds)].dir + fmt.Sprintf("/page-%d", j)
	rel, err := filepath.Rel(filepath.Dir(fromSlug), toSlug)
	if err != nil {
		rel = toSlug
	}
	return filepath.ToSlash(rel) + ".md"
}

func gen(out string, n int) error {
	// A fixed base time keeps runs reproducible; each page gets a distinct
	// timestamp so wiki_recent has a real ordering to return.
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	dirs := map[string]bool{}
	for i := 0; i < n; i++ {
		k := kinds[i%len(kinds)]
		dir := filepath.Join(out, k.dir)
		if !dirs[dir] {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			dirs[dir] = true
		}
		ts := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		// Cross-link to a few nearby pages by relative .md path, the real-bundle
		// idiom. relLink computes a correct path across the mixed directory depths.
		fromSlug := k.dir + fmt.Sprintf("/page-%d", i)
		a, b := (i+1)%n, (i+7)%n
		body := fmt.Sprintf("# Page %d\n\nType %s. See "+
			"[page %d](%s), [page %d](%s).\n\n"+
			"Searchable body text for page %d: quokka lorem ipsum dolor sit amet.\n",
			i, k.typ,
			a, relLink(fromSlug, a), b, relLink(fromSlug, b), i)
		fm := fmt.Sprintf("---\ntitle: Page %d\ntype: %s\n"+
			"description: synthetic OKF concept page %d\n"+
			"resource: https://example.com/%s/page-%d\n"+
			"tags: [tag-%d, tag-%d, %s]\ntimestamp: %q\n---\n",
			i, k.typ, i, k.dir, i, i%97, i%13, k.dir, ts)
		p := filepath.Join(dir, fmt.Sprintf("page-%d.md", i))
		if err := os.WriteFile(p, []byte(fm+body), 0o644); err != nil {
			return err
		}
	}

	// A root index.md (reserved, no frontmatter) linking each subdirectory.
	idx := "# Subdirectories\n\n"
	for d := range dirs {
		rel, _ := filepath.Rel(out, d)
		idx += fmt.Sprintf("* [%s](%s/index.md)\n", rel, rel)
	}
	if err := os.WriteFile(filepath.Join(out, "index.md"), []byte(idx), 0o644); err != nil {
		return err
	}
	// A config enabling OKF mode.
	if err := os.MkdirAll(filepath.Join(out, ".waqwaq"), 0o755); err != nil {
		return err
	}
	cfg := `{"title":"Synthetic OKF Wiki","mcp_description":"a large synthetic OKF bundle for scale testing"}`
	return os.WriteFile(filepath.Join(out, ".waqwaq", "config.json"), []byte(cfg), 0o644)
}
