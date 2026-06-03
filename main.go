package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/msradam/waqwaq/internal/auth"
	"github.com/msradam/waqwaq/internal/config"
	"github.com/msradam/waqwaq/internal/mcpserver"
	"github.com/msradam/waqwaq/internal/render"
	"github.com/msradam/waqwaq/internal/review"
	"github.com/msradam/waqwaq/internal/search"
	"github.com/msradam/waqwaq/internal/server"
	"github.com/msradam/waqwaq/internal/store"
)

const version = "0.2.0"

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		cmdServe(os.Args[2:])
	case "init":
		cmdInit(os.Args[2:])
	case "ingest":
		cmdIngest(os.Args[2:])
	case "export":
		cmdExport(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println("waqwaq", version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `waqwaq is a git-backed markdown wiki that humans browse and AI agents read and write.

usage:
  waqwaq init   [dir]                 scaffold a new wiki (wiki/ + raw/ + CLAUDE.md)
  waqwaq serve  [dir] [--addr] [--read-only] [--review] [--tokens FILE]
                                      serve web UI + MCP over one port
  waqwaq ingest <dir> <file>...       add raw documents to the wiki's raw/ area
  waqwaq export <dir> <outdir>        render the wiki to a static HTML site
  waqwaq version

Pages are served from <dir>/wiki if present, otherwise from <dir> itself, so a
bare markdown folder or an Obsidian vault works without restructuring.

`)
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8000", "address to listen on")
	readOnly := fs.Bool("read-only", envBool("WAQWAQ_READ_ONLY"), "disable writes (AI and human)")
	forceReview := fs.Bool("review", envBool("WAQWAQ_REVIEW"), "queue every write for human review")
	tokensPath := fs.String("tokens", "", "path to a JSON tokens file (default <dir>/.waqwaq/tokens.json)")
	rest := parseArgs(fs, args)

	dirs := rest
	if len(dirs) == 0 {
		dirs = []string{"."}
	}
	setFlags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { setFlags[f.Name] = true })

	var handler http.Handler
	if len(dirs) == 1 {
		tp := *tokensPath
		if tp == "" {
			tp = os.Getenv("WAQWAQ_TOKENS")
		}
		srv, cleanup, cfg, reg, err := buildWiki(dirs[0], "", *readOnly, *forceReview, tp, nil)
		if err != nil {
			log.Fatalf("serve: %v", err)
		}
		defer cleanup()
		if !setFlags["addr"] && cfg.Addr != "" {
			*addr = cfg.Addr
		}
		handler = srv.Handler()

		mode := "read-write"
		if *readOnly {
			mode = "read-only"
		}
		authMode := "open"
		if reg.Enabled() {
			authMode = "token"
		}
		policy := "direct commit"
		switch {
		case *forceReview || cfg.Review:
			policy = "all writes to review queue"
		case reg.Enabled():
			policy = "trusted commit, others to review queue"
		}
		log.Printf("waqwaq %s  ·  %s  ·  %s", version, dirs[0], mode)
		log.Printf("  web UI : http://%s/", *addr)
		log.Printf("  MCP    : http://%s/mcp   (auth: %s, writes: %s)", *addr, authMode, policy)
		if cfg.Web.ProxyHeader != "" {
			log.Printf("  web auth: identity from proxy header %q", cfg.Web.ProxyHeader)
		}
	} else {
		wikis := make([]server.WikiRef, len(dirs))
		for i, d := range dirs {
			name := filepath.Base(d)
			wikis[i] = server.WikiRef{Name: name, Base: "/w/" + name}
		}
		mux := http.NewServeMux()
		for i, d := range dirs {
			srv, cleanup, _, _, err := buildWiki(d, wikis[i].Base, *readOnly, *forceReview, "", wikis)
			if err != nil {
				log.Fatalf("serve %s: %v", d, err)
			}
			defer cleanup()
			mux.Handle(wikis[i].Base+"/", http.StripPrefix(wikis[i].Base, srv.Handler()))
		}
		if sfs, err := server.StaticFS(); err == nil {
			mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(sfs))))
		}
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
		landing := template.Must(template.New("landing").Parse(farmLandingHTML))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = landing.Execute(w, wikis)
		})
		handler = mux

		log.Printf("waqwaq %s  ·  farm of %d wikis", version, len(dirs))
		log.Printf("  web UI : http://%s/", *addr)
		for _, ww := range wikis {
			log.Printf("  · %-16s http://%s%s/   (MCP at %s/mcp)", ww.Name, *addr, ww.Base, ww.Base)
		}
	}

	httpSrv := &http.Server{Addr: *addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

// buildWiki assembles one wiki's full stack (store, auth, review queue, search
// index, MCP server, web server) for a data directory. base is "" for a single
// wiki at the root, or "/w/<name>" in farm mode. The returned cleanup closes the
// search index.
func buildWiki(dir, base string, readOnly, forceReview bool, tokensPath string, wikis []server.WikiRef) (*server.Server, func(), config.Config, *auth.Registry, error) {
	cleanup := func() {}
	st, err := store.New(dir)
	if err != nil {
		return nil, cleanup, config.Config{}, nil, err
	}
	tp := tokensPath
	if tp == "" {
		tp = filepath.Join(st.Root(), ".waqwaq", "tokens.json")
	}
	reg, err := auth.Load(tp)
	if err != nil {
		return nil, cleanup, config.Config{}, nil, err
	}
	cfg, err := config.Load(filepath.Join(st.Root(), ".waqwaq", "config.json"))
	if err != nil {
		return nil, cleanup, config.Config{}, nil, err
	}
	q, err := review.New(st, cfg.Webhook)
	if err != nil {
		return nil, cleanup, config.Config{}, nil, err
	}
	var searcher search.Searcher = st
	if idx, err := search.New(st); err == nil {
		searcher = idx
		cleanup = func() { _ = idx.Close() }
	}
	queueAll := forceReview || cfg.Review
	mcpSrv := mcpserver.New(st, q, reg, mcpserver.Options{ReadOnly: readOnly, ForceReview: queueAll, Rules: cfg.Lint, Search: searcher})
	srv, err := server.New(server.Options{
		Store: st, Renderer: render.New(), MCP: mcpSrv, Auth: reg, Queue: q, Search: searcher, Rules: cfg.Lint,
		Web:      server.WebPolicy{ProxyHeader: cfg.Web.ProxyHeader, DefaultRole: cfg.Web.DefaultRole, Admins: cfg.Web.Admins, Editors: cfg.Web.Editors},
		ReadOnly: readOnly,
		Site:     server.Site{Title: cfg.Title, Accent: cfg.Accent, Theme: cfg.Theme},
		Base:     base,
		Wikis:    wikis,
	})
	if err != nil {
		cleanup()
		return nil, func() {}, config.Config{}, nil, err
	}
	return srv, cleanup, cfg, reg, nil
}

const farmLandingHTML = `<!doctype html>
<html lang="en" data-theme="auto">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Waqwaq</title>
<meta name="color-scheme" content="light dark">
<link rel="icon" type="image/svg+xml" href="/static/favicon.svg">
<link rel="stylesheet" href="/static/style.css">
</head>
<body>
<main class="content" style="max-width:600px;margin:60px auto">
  <h1>🌳 Waqwaq</h1>
  <p class="muted">Wikis on this server:</p>
  <ul class="results">
    {{range .}}<li><a href="{{.Base}}/">{{.Name}}</a></li>{{end}}
  </ul>
</main>
</body>
</html>`

func cmdInit(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	rest := parseArgs(fs, args)
	dir := "."
	if len(rest) > 0 {
		dir = rest[0]
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	for _, sub := range []string{"wiki", "raw"} {
		if err := os.MkdirAll(filepath.Join(abs, sub), 0o755); err != nil {
			log.Fatalf("init: %v", err)
		}
	}
	schemaPath := filepath.Join(abs, "CLAUDE.md")
	if _, err := os.Stat(schemaPath); os.IsNotExist(err) {
		if err := os.WriteFile(schemaPath, []byte(sampleSchema), 0o644); err != nil {
			log.Fatalf("init: %v", err)
		}
	}
	st, err := store.New(abs) // detects the wiki/ subdir created above
	if err != nil {
		log.Fatalf("init: %v", err)
	}
	samples := []struct{ slug, content string }{
		{"index", sampleIndex},
		{"concepts/mcp", sampleMCP},
	}
	for _, s := range samples {
		if _, err := st.Read(s.slug); err == nil {
			continue
		}
		if err := st.Write(s.slug, s.content, "waqwaq <init@waqwaq.local>", "waqwaq: scaffold "+s.slug); err != nil {
			log.Fatalf("write %s: %v", s.slug, err)
		}
	}
	fmt.Printf("Initialised Waqwaq wiki in %s\n", abs)
	fmt.Print("  wiki/      markdown pages\n  raw/       raw documents to synthesise from\n  CLAUDE.md  wiki schema\n")
	fmt.Printf("Next: waqwaq serve %s\n", dir)
}

func cmdIngest(args []string) {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	rest := parseArgs(fs, args)
	if len(rest) < 2 {
		log.Fatal("usage: waqwaq ingest <dir> <file>...")
	}
	st, err := store.New(rest[0])
	if err != nil {
		log.Fatalf("ingest: %v", err)
	}
	for _, f := range rest[1:] {
		data, err := os.ReadFile(f)
		if err != nil {
			log.Fatalf("read %s: %v", f, err)
		}
		name := filepath.Base(f)
		if err := st.AddRaw(name, data); err != nil {
			log.Fatalf("add raw: %v", err)
		}
		fmt.Printf("ingested %s -> raw/%s\n", f, name)
	}
	fmt.Println("Agents can read these via wiki_list_raw / wiki_read_raw,")
	fmt.Println("then synthesise pages with wiki_write (lint runs before each write lands).")
}

var wikiLinkHTML = regexp.MustCompile(`(href|src)="/wiki/([^"#]+)(#[^"]*)?"`)

func cmdExport(args []string) {
	fset := flag.NewFlagSet("export", flag.ExitOnError)
	rest := parseArgs(fset, args)
	if len(rest) < 2 {
		log.Fatal("usage: waqwaq export <dir> <outdir>")
	}
	st, err := store.New(rest[0])
	if err != nil {
		log.Fatalf("export: %v", err)
	}
	out := rest[1]
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatalf("export: %v", err)
	}
	metas, err := st.List()
	if err != nil {
		log.Fatalf("export: %v", err)
	}
	rnd := render.New()
	tmpl := template.Must(template.New("static").Parse(staticPageHTML))
	site := "Waqwaq"
	if cfg, err := config.Load(filepath.Join(st.Root(), ".waqwaq", "config.json")); err == nil && cfg.Title != "" {
		site = cfg.Title
	}

	for _, m := range metas {
		page, err := st.Read(m.Slug)
		if err != nil {
			continue
		}
		html, _, err := rnd.Render(page.Body)
		if err != nil {
			continue
		}
		rewritten := template.HTML(wikiLinkHTML.ReplaceAllString(string(html), `${1}="/${2}.html${3}"`)) //nolint:gosec
		var buf bytes.Buffer
		_ = tmpl.Execute(&buf, map[string]any{"Title": page.Title, "Site": site, "Content": rewritten, "Pages": metas})
		dest := filepath.Join(out, filepath.FromSlash(m.Slug)+".html")
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			log.Fatalf("export: %v", err)
		}
		if err := os.WriteFile(dest, buf.Bytes(), 0o644); err != nil {
			log.Fatalf("export: %v", err)
		}
	}

	copyDir(filepath.Join(st.Root(), "assets"), filepath.Join(out, "assets"))
	if sfs, err := server.StaticFS(); err == nil {
		_ = os.MkdirAll(filepath.Join(out, "static"), 0o755)
		for _, name := range []string{"style.css", "favicon.svg"} {
			if data, err := fs.ReadFile(sfs, name); err == nil {
				_ = os.WriteFile(filepath.Join(out, "static", name), data, 0o644)
			}
		}
	}
	fmt.Printf("Exported %d pages to %s\n", len(metas), out)
	fmt.Printf("Serve it with any static file server, e.g. python3 -m http.server -d %s\n", out)
}

func copyDir(src, dst string) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	_ = os.MkdirAll(dst, 0o755)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if data, err := os.ReadFile(filepath.Join(src, e.Name())); err == nil {
			_ = os.WriteFile(filepath.Join(dst, e.Name()), data, 0o644)
		}
	}
}

const staticPageHTML = `<!doctype html>
<html lang="en" data-theme="auto">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · {{.Site}}</title>
<meta name="color-scheme" content="light dark">
<link rel="icon" type="image/svg+xml" href="/static/favicon.svg">
<link rel="stylesheet" href="/static/style.css">
</head>
<body>
<div class="layout">
  <aside class="sidebar">
    <a class="brand" href="/index.html">🌳 {{.Site}}</a>
    <nav>{{range .Pages}}<a class="nav-link" href="/{{.Slug}}.html">{{.Title}}</a>{{end}}</nav>
  </aside>
  <main class="content"><article class="markdown">{{.Content}}</article></main>
</div>
<script type="module">
  import mermaid from 'https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs';
  mermaid.initialize({ startOnLoad: true, theme: 'neutral' });
</script>
</body>
</html>
`

// parseArgs parses flags that may appear before, after, or between positional
// arguments, returning the positionals in order. The stdlib flag package stops
// at the first positional, so we resume parsing after each one.
func parseArgs(fs *flag.FlagSet, args []string) []string {
	var positionals []string
	for {
		_ = fs.Parse(args)
		if fs.NArg() == 0 {
			return positionals
		}
		positionals = append(positionals, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func envBool(key string) bool {
	switch os.Getenv(key) {
	case "1", "true", "TRUE", "yes":
		return true
	}
	return false
}

const sampleIndex = `---
title: Home
---

# Welcome to Waqwaq

A git-backed markdown wiki that **humans browse** and **AI agents read and write**, served from one binary over one port.

- Browse pages from the sidebar.
- Point an MCP client at ` + "`/mcp`" + ` to let an agent read and update these pages.
- Every write runs through lint and lands as a git commit, so nothing rots silently.

` + "```mermaid" + `
flowchart LR
  Human([Human]) -->|browse / approve| Wiki[(Markdown + git)]
  Agent([AI agent]) -->|MCP read / write| Wiki
  Wiki -->|lint on write| Agent
` + "```" + `

See [[concepts/mcp]] for how an agent connects.
`

const sampleMCP = `---
title: MCP integration
---

# MCP integration

Waqwaq serves a Model Context Protocol endpoint at ` + "`/mcp`" + ` (streamable HTTP) from the same
process as the web UI.

## Tools

- Read the wiki with ` + "`wiki_list`" + `, ` + "`wiki_read`" + `, ` + "`wiki_search`" + `, and ` + "`wiki_graph`" + `.
- Work with raw documents under ` + "`raw/`" + ` using ` + "`wiki_list_raw`" + `, ` + "`wiki_read_raw`" + `, and ` + "`wiki_ingest`" + `.
- Dry-run the write checks with ` + "`wiki_lint`" + `.
- Create or replace a page with ` + "`wiki_write`" + `. Lint runs first; errors block the write.

## Example handler

` + "```go" + `
mcp.AddTool(s, &mcp.Tool{Name: "wiki_read"}, func(
	ctx context.Context, req *mcp.CallToolRequest, in readIn,
) (*mcp.CallToolResult, readOut, error) {
	page, err := st.Read(in.Slug)
	if err != nil {
		return nil, readOut{}, err
	}
	return nil, readOut{Slug: page.Slug, Title: page.Title, Content: page.Raw}, nil
})
` + "```" + `

Back to [[index]].
`

const sampleSchema = `# Wiki schema

This is a Waqwaq wiki. The source of truth is markdown under ` + "`wiki/`" + `, versioned with git.
Raw documents to synthesise pages from live under ` + "`raw/`" + `.

## Pages

- One concept per page. The file path under ` + "`wiki/`" + ` is the slug.
- Begin each page with YAML frontmatter containing a ` + "`title`" + `.
- Link between pages with ` + "`[[slug]]`" + ` or ` + "`[[slug|label]]`" + ` wikilinks.

## Writing via MCP

- Read with ` + "`wiki_list`" + `, ` + "`wiki_read`" + `, ` + "`wiki_search`" + `, ` + "`wiki_graph`" + `.
- Add raw documents with ` + "`wiki_ingest`" + `; read them with ` + "`wiki_list_raw`" + ` and ` + "`wiki_read_raw`" + `.
- Create or replace pages with ` + "`wiki_write`" + `. Lint runs first; a missing title blocks the write.
- Depending on your access, a write either commits or is queued for review. Check the returned status and the queue with ` + "`wiki_list_proposals`" + `.
`
