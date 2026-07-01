// Command waqwaq serves a read-only OKF markdown wiki over MCP, a web view, and
// a terminal view — all backed by one stateless core over the same files.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/msradam/waqwaq/core"
	"github.com/msradam/waqwaq/internal/config"
	"github.com/msradam/waqwaq/internal/mcpserver"
	"github.com/msradam/waqwaq/internal/server"
	"github.com/msradam/waqwaq/internal/tui"
	versionpkg "github.com/msradam/waqwaq/internal/version"
)

const version = versionpkg.Version

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
	case "mcp":
		cmdMCP(os.Args[2:])
	case "tui":
		cmdTUI(os.Args[2:])
	case "doctor":
		cmdDoctor(os.Args[2:])
	case "validate", "check":
		cmdValidate(os.Args[2:])
	case "toc":
		cmdTOC(os.Args[2:])
	case "recent":
		cmdRecent(os.Args[2:])
	case "grep":
		cmdGrep(os.Args[2:])
	case "cat":
		cmdCat(os.Args[2:])
	case "init":
		cmdInit(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println("waqwaq", version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `waqwaq serves a read-only OKF markdown wiki over MCP, web, and a terminal view.

usage:
  waqwaq serve  [dir] [--addr]   serve the web view + MCP endpoint on one port
  waqwaq mcp    [dir]            serve MCP over stdio (for an agent subprocess)
  waqwaq tui    [dir]            browse the wiki in a terminal reader
  waqwaq doctor [dir]            check the wiki's setup and MCP posture
  waqwaq validate [dir] [--json] [--strict]   check OKF compliance and links, for CI
  waqwaq toc    [dir]            list pages as slug<tab>title (greppable)
  waqwaq recent [dir] [--limit]  list pages by OKF timestamp, freshest first
  waqwaq grep   <query> [dir]    full-text search; --regex for a pattern
  waqwaq cat    <slug> [dir]     print a page; --render for terminal markdown
  waqwaq init   <dir>            scaffold a small OKF wiki
  waqwaq version

Pages are served from <dir>/wiki if present, otherwise from <dir> itself, so a
bare markdown folder or an OKF bundle works without restructuring. This baseline
is read-only across every surface: there are no write or mutation commands.
`)
}

// buildCore loads config and builds a Core for a directory. OKF enforcement is
// on unless the wiki opts out with "lenient": true.
func buildCore(dir string) (core.Core, config.Config, error) {
	if dir == "" {
		dir = "."
	}
	cfg, err := config.Load(dir)
	if err != nil {
		return nil, cfg, err
	}
	c, err := core.New(dir, !cfg.Lenient)
	return c, cfg, err
}

// enforceOKF refuses to start a server on a bundle that is not OKF compliant,
// unless the wiki is lenient. This is the opinionated stake on the format: a
// waqwaq server serves OKF, or it does not serve. Broken links are reported but
// tolerated (SPEC §9 forbids rejecting a bundle for them).
func enforceOKF(c core.Core, cfg config.Config, lenientFlag bool) {
	if cfg.Lenient || lenientFlag {
		return
	}
	rep, err := core.Validate(context.Background(), c, false)
	if err != nil {
		log.Fatalf("okf: %v", err)
	}
	if !rep.OK() {
		fmt.Fprintf(os.Stderr, "refusing to serve: %d of %d pages are not OKF compliant.\n", len(rep.Compliance), rep.Pages)
		for _, v := range rep.Compliance {
			fmt.Fprintln(os.Stderr, "  "+v)
		}
		fmt.Fprintln(os.Stderr, "\nEvery concept page needs a non-empty `type` (OKF SPEC §4.1).")
		fmt.Fprintln(os.Stderr, "Run `waqwaq validate` for the full report, or set \"lenient\": true / pass --lenient to serve non-OKF markdown.")
		os.Exit(1)
	}
	if len(rep.BrokenLinks) > 0 {
		log.Printf("  note: %d broken link(s); serving anyway (run `waqwaq validate` to list them)", len(rep.BrokenLinks))
	}
}

// instructionsFor returns the CLAUDE.md schema if the Core exposes one.
func instructionsFor(c core.Core) string {
	if ins, ok := c.(interface{ Instructions() string }); ok {
		return ins.Instructions()
	}
	return ""
}

func newMCP(c core.Core, cfg config.Config) *mcp.Server {
	return mcpserver.New(c, mcpserver.Options{
		Title:        cfg.Title,
		Description:  cfg.Description,
		Instructions: instructionsFor(c),
	})
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8000", "address to listen on")
	lenient := fs.Bool("lenient", false, "serve even if the wiki is not OKF compliant")
	rest := parseArgs(fs, args)
	dir := "."
	if len(rest) > 0 {
		dir = rest[0]
	}

	c, cfg, err := buildCore(dir)
	if err != nil {
		log.Fatalf("serve: %v", err)
	}
	enforceOKF(c, cfg, *lenient)
	if cfg.Addr != "" && !flagSet(fs, "addr") {
		*addr = cfg.Addr
	}
	// Warm the cache off the request path so the first client on a large wiki
	// doesn't pay the initial parse.
	go func() { _, _ = c.List(context.Background(), "", "", "", 1, 0) }()

	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return newMCP(c, cfg) }, nil)
	srv := server.New(c, mcpHandler, server.Site{Title: cfg.Title})

	httpSrv := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	log.Printf("waqwaq %s  ·  %s  ·  read-only", version, dir)
	log.Printf("  web view : http://%s/", *addr)
	log.Printf("  MCP      : http://%s/mcp", *addr)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

func cmdMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	lenient := fs.Bool("lenient", false, "serve even if the wiki is not OKF compliant")
	rest := parseArgs(fs, args)
	dir := "."
	if len(rest) > 0 {
		dir = rest[0]
	}
	c, cfg, err := buildCore(dir)
	if err != nil {
		log.Fatalf("mcp: %v", err)
	}
	enforceOKF(c, cfg, *lenient)
	if err := mcpserver.ServeStdio(context.Background(), newMCP(c, cfg)); err != nil && !errors.Is(err, io.EOF) {
		log.Fatalf("mcp: %v", err)
	}
}

func cmdTUI(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	rest := parseArgs(fs, args)
	dir := "."
	if len(rest) > 0 {
		dir = rest[0]
	}
	c, _, err := buildCore(dir)
	if err != nil {
		log.Fatalf("tui: %v", err)
	}
	if err := tui.Run(c); err != nil {
		log.Fatalf("tui: %v", err)
	}
}

func cmdDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	rest := parseArgs(fs, args)
	dir := "."
	if len(rest) > 0 {
		dir = rest[0]
	}
	c, cfg, err := buildCore(dir)
	if err != nil {
		log.Fatalf("doctor: %v", err)
	}
	ctx := context.Background()

	check := func(ok bool, label, okMsg, warnMsg string) {
		mark := "warn"
		msg := warnMsg
		if ok {
			mark, msg = "ok", okMsg
		}
		fmt.Printf("[%s] %-10s %s\n", mark, label, msg)
	}

	res, err := c.List(ctx, "", "", "", 0, 0)
	if err != nil {
		log.Fatalf("doctor: %v", err)
	}
	check(res.Total > 0, "pages", fmt.Sprintf("%d concept pages", res.Total), "no pages found")

	check(core.IsGitRepo(dir), "git", "git repo; page history available", "not a git repo; history unavailable")

	check(instructionsFor(c) != "", "schema", "CLAUDE.md present; agents receive it", "no CLAUDE.md; agents connect without a schema")
	check(cfg.Title != "", "identity", "title set: "+cfg.Title, "no title; MCP server identity defaults to \"wiki\"")

	if cfg.Lenient {
		fmt.Println("[warn] okf        lenient mode; OKF compliance NOT enforced")
	} else {
		fmt.Println("[ok] okf        OKF enforced; every concept page needs a type")
	}
}

// cmdValidate checks a bundle against the OKF spec (SPEC §9): every non-reserved
// concept page has a non-empty `type` (a compliance error), plus broken links
// (warnings, tolerated per §9). --strict also flags missing recommended fields
// (title/description/timestamp), the profile Google's reference agent emits.
// Exits non-zero on any compliance error or broken link, so it works in CI.
func cmdValidate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON")
	strict := fs.Bool("strict", false, "also require title, description, timestamp (Google reference-agent profile)")
	rest := parseArgs(fs, args)
	dir := "."
	if len(rest) > 0 {
		dir = rest[0]
	}
	c, _, err := buildCore(dir)
	if err != nil {
		log.Fatalf("validate: %v", err)
	}
	rep, err := core.Validate(context.Background(), c, *strict)
	if err != nil {
		log.Fatalf("validate: %v", err)
	}

	if *asJSON {
		fmt.Printf("{\"pages\":%d,\"compliant\":%t,\"violations\":%d,\"broken_links\":%d,\"strict_misses\":%d}\n",
			rep.Pages, rep.OK(), len(rep.Compliance), len(rep.BrokenLinks), len(rep.StrictMisses))
	} else {
		for _, v := range rep.Compliance {
			fmt.Println("error:  " + v)
		}
		for _, v := range rep.StrictMisses {
			fmt.Println("strict: " + v)
		}
		for _, v := range rep.BrokenLinks {
			fmt.Println("warn:   " + v)
		}
		status := "OKF compliant"
		if !rep.OK() {
			status = "NOT OKF compliant"
		}
		fmt.Printf("%d concept pages — %s (%d violations, %d broken links)\n",
			rep.Pages, status, len(rep.Compliance), len(rep.BrokenLinks))
	}
	if !rep.OK() || len(rep.BrokenLinks) > 0 || len(rep.StrictMisses) > 0 {
		os.Exit(1)
	}
}

func cmdTOC(args []string) {
	fs := flag.NewFlagSet("toc", flag.ExitOnError)
	rest := parseArgs(fs, args)
	dir := "."
	if len(rest) > 0 {
		dir = rest[0]
	}
	c, _, err := buildCore(dir)
	if err != nil {
		log.Fatalf("toc: %v", err)
	}
	res, err := c.List(context.Background(), "", "", "", allPages, 0)
	if err != nil {
		log.Fatalf("toc: %v", err)
	}
	for _, p := range res.Items {
		fmt.Printf("%s\t%s\n", p.Slug, p.Title)
	}
}

// allPages is a limit large enough to return every page from List, for CLI verbs
// that dump the whole wiki (List caps the slice at the true total).
const allPages = 1 << 30

func cmdRecent(args []string) {
	fs := flag.NewFlagSet("recent", flag.ExitOnError)
	limit := fs.Int("limit", 20, "how many pages to show")
	rest := parseArgs(fs, args)
	dir := "."
	if len(rest) > 0 {
		dir = rest[0]
	}
	c, _, err := buildCore(dir)
	if err != nil {
		log.Fatalf("recent: %v", err)
	}
	pages, err := c.Recent(context.Background(), *limit)
	if err != nil {
		log.Fatalf("recent: %v", err)
	}
	for _, p := range pages {
		ts := p.Timestamp
		if ts == "" {
			ts = "-"
		}
		fmt.Printf("%s\t%s\t%s\n", ts, p.Slug, p.Title)
	}
}

func cmdGrep(args []string) {
	fs := flag.NewFlagSet("grep", flag.ExitOnError)
	asRegex := fs.Bool("regex", false, "treat query as a regular expression")
	typeFilter := fs.String("type", "", "narrow to an OKF type")
	tagFilter := fs.String("tag", "", "narrow to a tag")
	rest := parseArgs(fs, args)
	if len(rest) == 0 || strings.TrimSpace(rest[0]) == "" {
		log.Fatal("usage: waqwaq grep <query> [dir]")
	}
	query := rest[0]
	dir := "."
	if len(rest) > 1 {
		dir = rest[1]
	}
	c, _, err := buildCore(dir)
	if err != nil {
		log.Fatalf("grep: %v", err)
	}
	ctx := context.Background()
	hits, err := c.Search(ctx, query, *asRegex, *typeFilter, *tagFilter, 0)
	if err != nil {
		log.Fatalf("grep: %v", err)
	}
	for _, h := range hits {
		fmt.Printf("%s\t%s\n", h.Slug, h.Context)
	}
	// A silent empty result behind a type/tag filter is confusing; when the filter
	// is the likely cause, show what values actually exist.
	if len(hits) == 0 && (*typeFilter != "" || *tagFilter != "") {
		if *typeFilter != "" {
			fmt.Fprintf(os.Stderr, "no matches; types present: %s\n", strings.Join(distinctTypes(ctx, c), ", "))
		}
		if *tagFilter != "" {
			if tags, err := c.Tags(ctx, nil); err == nil {
				names := make([]string, len(tags))
				for i, t := range tags {
					names[i] = t.Tag
				}
				fmt.Fprintf(os.Stderr, "no matches; tags present: %s\n", strings.Join(names, ", "))
			}
		}
	}
}

// distinctTypes returns the sorted set of OKF types present in the wiki, for
// hinting when a --type filter matched nothing.
func distinctTypes(ctx context.Context, c core.Core) []string {
	res, err := c.List(ctx, "", "", "", 0, 0)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var types []string
	for _, p := range res.Items {
		if p.Type != "" && !seen[p.Type] {
			seen[p.Type] = true
			types = append(types, p.Type)
		}
	}
	sort.Strings(types)
	return types
}

func cmdCat(args []string) {
	fs := flag.NewFlagSet("cat", flag.ExitOnError)
	doRender := fs.Bool("render", false, "render markdown for the terminal")
	rest := parseArgs(fs, args)
	if len(rest) == 0 {
		log.Fatal("usage: waqwaq cat <slug> [dir]")
	}
	slug := rest[0]
	dir := "."
	if len(rest) > 1 {
		dir = rest[1]
	}
	c, _, err := buildCore(dir)
	if err != nil {
		log.Fatalf("cat: %v", err)
	}
	page, err := c.Read(context.Background(), slug)
	if err != nil {
		log.Fatalf("cat: %v", err)
	}
	if *doRender {
		out, err := glamour.Render(page.Body, glamourStyle())
		if err != nil {
			log.Fatalf("cat: %v", err)
		}
		fmt.Print(out)
		return
	}
	fmt.Println(page.Body)
}

func glamourStyle() string {
	if strings.Contains(strings.ToLower(os.Getenv("COLORFGBG")), "15;") {
		return "light"
	}
	return "dark"
}

func cmdInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	rest := parseArgs(fs, args)
	if len(rest) == 0 {
		log.Fatal("usage: waqwaq init <dir>")
	}
	abs, err := filepath.Abs(rest[0])
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, "wiki"), 0o755); err != nil {
		log.Fatalf("init: %v", err)
	}
	files := map[string]string{
		"CLAUDE.md":                sampleSchema,
		"wiki/index.md":            sampleIndex,
		"wiki/datasets/example.md": sampleDataset,
	}
	for rel, content := range files {
		p := filepath.Join(abs, rel)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			log.Fatalf("init: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			log.Fatalf("init: %v", err)
		}
	}
	fmt.Printf("Initialised a waqwaq wiki in %s\n", abs)
	fmt.Printf("Next: waqwaq serve %s\n", rest[0])
}

// parseArgs parses flags that may appear before or after positional arguments.
// Go's flag package stops at the first non-flag token, so `serve <dir> --addr x`
// would otherwise drop --addr. This interleaves: parse flags, take one
// positional, repeat.
func parseArgs(fs *flag.FlagSet, args []string) []string {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return positionals
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positionals
		}
		positionals = append(positionals, rest[0])
		args = rest[1:]
	}
}

func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

const sampleSchema = `# Wiki schema

A read-only OKF wiki served by waqwaq. Pages are markdown under ` + "`wiki/`" + `,
versioned with git. Each concept page carries YAML frontmatter with a ` + "`type`" + `
and may set ` + "`description`" + `, ` + "`resource`" + ` (an external asset URL),
` + "`tags`" + `, and ` + "`timestamp`" + `. Link pages with ` + "`[[slug]]`" + `
wikilinks or relative ` + "`.md`" + ` links.
`

const sampleIndex = `---
title: Example Catalog
type: Index
description: Entry point for this example OKF wiki.
---

# Example Catalog

A small OKF-structured catalog served read-only by waqwaq.

## Datasets

- [[datasets/example]] — an example dataset
`

const sampleDataset = `---
title: Example Dataset
type: Dataset
description: An example dataset to show OKF frontmatter.
resource: https://example.com/datasets/example
tags: [example, demo]
timestamp: 2026-01-01T00:00:00Z
---

# Example Dataset

Replace this with a real dataset. Back to [[index]].
`
