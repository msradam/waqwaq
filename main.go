package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/glamour"
	"golang.org/x/crypto/bcrypt"

	"github.com/msradam/waqwaq/internal/auth"
	"github.com/msradam/waqwaq/internal/config"
	"github.com/msradam/waqwaq/internal/ingest"
	"github.com/msradam/waqwaq/internal/kb"
	"github.com/msradam/waqwaq/internal/kbclient"
	"github.com/msradam/waqwaq/internal/lint"
	"github.com/msradam/waqwaq/internal/mcpserver"
	"github.com/msradam/waqwaq/internal/render"
	"github.com/msradam/waqwaq/internal/review"
	"github.com/msradam/waqwaq/internal/search"
	"github.com/msradam/waqwaq/internal/server"
	"github.com/msradam/waqwaq/internal/store"
	"github.com/msradam/waqwaq/internal/tui"
)

const version = "0.4.0"

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
	case "scan":
		cmdScan(os.Args[2:])
	case "doctor":
		cmdDoctor(os.Args[2:])
	case "check":
		cmdCheck(os.Args[2:])
	case "mcp":
		cmdMCP(os.Args[2:])
	case "toc":
		cmdTOC(os.Args[2:])
	case "grep":
		cmdGrep(os.Args[2:])
	case "cat":
		cmdCat(os.Args[2:])
	case "tui":
		cmdTUI(os.Args[2:])
	case "passwd":
		cmdPasswd(os.Args[2:])
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
  waqwaq init   <dir>                 scaffold a new wiki (wiki/ + raw/ + CLAUDE.md)
  waqwaq serve  [dir] [--addr] [--read-only] [--review] [--tokens FILE]
                                      serve web UI + MCP over one port
  waqwaq ingest <dir> <file>...       add raw documents to the wiki's raw/ area
  waqwaq export <dir> <outdir>        render the wiki to a static HTML site
  waqwaq scan   <repo> <outdir>       generate a wiki from a Go module's import graph
  waqwaq doctor [dir]                 check a wiki's setup, MCP access, and health
  waqwaq check  [dir] [--json] [--strict]   lint pages and links, for CI (non-zero on errors)
  waqwaq mcp    [dir]                 serve MCP over stdio (for an agent subprocess)
  waqwaq toc    [dir]                 list pages as slug<tab>title (greppable)
  waqwaq grep   <query> [dir]         full-text search; --tag, --links-to scope it
  waqwaq cat    <slug> [dir]          print a page; --render for terminal markdown
  waqwaq tui    [dir]                 browse the wiki in a terminal reader
  waqwaq passwd [password]            print a bcrypt hash for a web.users entry
  waqwaq version

The query verbs (toc, grep, cat) run against a local folder, or add
--remote URL (or set WAQWAQ_REMOTE) to query a running waqwaq server's /api.

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
		} else if len(cfg.Web.Users) > 0 {
			log.Printf("  web auth: built-in login (%d user(s))", len(cfg.Web.Users))
		}
		webAuth := cfg.Web.ProxyHeader != "" || len(cfg.Web.Users) > 0
		if webAuth && !reg.Enabled() && !*readOnly {
			log.Printf("  warning: web UI requires login but the MCP endpoint is open; add %s/.waqwaq/tokens.json to require a bearer token", dirs[0])
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

// buildWiki assembles one wiki's full stack for a data directory. base is "" for
// a single wiki at the root, or "/w/<name>" in farm mode. The returned cleanup
// closes the search index.
func buildWiki(dir, base string, readOnly, forceReview bool, tokensPath string, wikis []server.WikiRef) (*server.Server, func(), config.Config, *auth.Registry, error) {
	cleanup := func() {}
	st, err := store.New(dir)
	if err != nil {
		return nil, cleanup, config.Config{}, nil, err
	}
	go st.Warm()
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
	users := make([]server.WebUser, 0, len(cfg.Web.Users))
	for _, u := range cfg.Web.Users {
		users = append(users, server.WebUser{Name: u.Name, Hash: u.Password, Role: u.Role})
	}
	srv, err := server.New(server.Options{
		Store: st, Renderer: render.New(base), MCP: mcpSrv, Auth: reg, Queue: q, Search: searcher, Rules: cfg.Lint,
		Web:      server.WebPolicy{ProxyHeader: cfg.Web.ProxyHeader, DefaultRole: cfg.Web.DefaultRole, Admins: cfg.Web.Admins, Editors: cfg.Web.Editors, Users: users},
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
	if len(rest) == 0 {
		log.Fatal("usage: waqwaq init <dir>\nrefusing to scaffold into the current directory; pass an explicit path")
	}
	dir := rest[0]
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
	rnd := render.New("")
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

func cmdPasswd(args []string) {
	var pw string
	if len(args) > 0 {
		pw = args[0]
	} else {
		fmt.Fprint(os.Stderr, "Password: ")
		sc := bufio.NewScanner(os.Stdin)
		if sc.Scan() {
			pw = sc.Text()
		}
	}
	if pw == "" {
		log.Fatal("passwd: empty password")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		log.Fatalf("passwd: %v", err)
	}
	fmt.Println(string(hash))
}

func cmdMCP(args []string) {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	readOnly := fs.Bool("read-only", envBool("WAQWAQ_READ_ONLY"), "disable writes")
	forceReview := fs.Bool("review", envBool("WAQWAQ_REVIEW"), "queue all writes for review")
	rest := parseArgs(fs, args)
	dir := "."
	if len(rest) > 0 {
		dir = rest[0]
	}
	st, err := store.New(dir)
	if err != nil {
		log.Fatalf("mcp: %v", err)
	}
	cfg, _ := config.Load(filepath.Join(st.Root(), ".waqwaq", "config.json"))
	reg, err := auth.Load(filepath.Join(st.Root(), ".waqwaq", "tokens.json"))
	if err != nil {
		log.Fatalf("mcp: %v", err)
	}
	q, err := review.New(st, cfg.Webhook)
	if err != nil {
		log.Fatalf("mcp: %v", err)
	}
	var searcher search.Searcher = st
	if idx, err := search.New(st); err == nil {
		searcher = idx
		defer func() { _ = idx.Close() }()
	}
	srv := mcpserver.New(st, q, reg, mcpserver.Options{
		ReadOnly: *readOnly, ForceReview: *forceReview || cfg.Review, Rules: cfg.Lint, Search: searcher,
	})
	if err := mcpserver.ServeStdio(context.Background(), srv); err != nil && !errors.Is(err, io.EOF) {
		log.Fatalf("mcp: %v", err)
	}
}

// kbFor returns the knowledge base a query verb runs against: a remote server's
// /api when remote is set (or WAQWAQ_REMOTE), otherwise the local folder.
func kbFor(remote, token, dir string) (kb.KnowledgeBase, error) {
	if remote != "" {
		return kbclient.New(remote, token), nil
	}
	if dir == "" {
		dir = "."
	}
	return store.New(dir)
}

func argAt(rest []string, i int) string {
	if i < len(rest) {
		return rest[i]
	}
	return ""
}

func refsJSON(metas []store.PageMeta) any {
	type ref struct {
		Slug  string `json:"slug"`
		Title string `json:"title"`
	}
	out := make([]ref, len(metas))
	for i, m := range metas {
		out[i] = ref{Slug: m.Slug, Title: m.Title}
	}
	return out
}

func printJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Fatalf("json: %v", err)
	}
	fmt.Println(string(b))
}

func remoteFlags(fs *flag.FlagSet) (*string, *string) {
	return fs.String("remote", os.Getenv("WAQWAQ_REMOTE"), "query a remote waqwaq server URL instead of a local dir"),
		fs.String("token", os.Getenv("WAQWAQ_TOKEN"), "bearer token for --remote")
}

type diag struct {
	level string // ok, warn, fail, info
	label string
	msg   string
}

// cmdDoctor checks a wiki's setup, MCP/access posture, and health, exiting
// non-zero on a problem. This is the "is this wired up right" check, distinct
// from content linting.
func cmdDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	rest := parseArgs(fs, args)
	dir := "."
	if len(rest) > 0 {
		dir = rest[0]
	}
	if reportDiags(runDoctor(dir)) > 0 {
		os.Exit(1)
	}
}

func runDoctor(dir string) []diag {
	var out []diag
	add := func(level, label, msg string) { out = append(out, diag{level, label, msg}) }

	if p, err := exec.LookPath("git"); err == nil {
		add("ok", "git", "found at "+p)
	} else {
		add("fail", "git", "not on PATH; history, attribution, and review need git")
	}

	st, err := store.New(dir)
	if err != nil {
		return append(out, diag{"fail", "wiki", err.Error()})
	}
	root := st.Root()

	metas, _ := st.List()
	mode := "vault mode (serving the folder itself)"
	if fi, e := os.Stat(filepath.Join(root, "wiki")); e == nil && fi.IsDir() {
		mode = "serving wiki/"
	}
	if len(metas) == 0 {
		add("warn", "pages", "no markdown pages found; Waqwaq indexes .md files")
	} else {
		add("ok", "pages", fmt.Sprintf("%d markdown pages, %s", len(metas), mode))
	}

	if st.Instructions() != "" {
		add("ok", "schema", "CLAUDE.md present; agents receive it as MCP instructions")
	} else {
		add("warn", "schema", "no CLAUDE.md; agents connect without a wiki schema")
	}

	var cfg config.Config
	cfgPath := filepath.Join(root, ".waqwaq", "config.json")
	if _, e := os.Stat(cfgPath); os.IsNotExist(e) {
		add("ok", "config", "no config.json; using defaults")
	} else if c, e := config.Load(cfgPath); e != nil {
		add("fail", "config", "config.json is invalid: "+e.Error())
	} else {
		cfg = c
		add("ok", "config", "config.json is valid")
	}

	if idx, e := search.New(st); e == nil {
		_ = idx.Close()
		add("ok", "search", "full-text search (SQLite FTS5)")
	} else {
		add("warn", "search", "substring search (the FTS5 index is not in this build)")
	}

	reg, tokErr := auth.Load(filepath.Join(root, ".waqwaq", "tokens.json"))
	mcpTokened := tokErr == nil && reg.Enabled()
	switch {
	case tokErr != nil:
		add("fail", "mcp", "tokens.json is invalid: "+tokErr.Error())
	case mcpTokened:
		add("ok", "mcp", "the MCP endpoint requires a bearer token")
	default:
		add("warn", "mcp", "the MCP endpoint is open; any caller that reaches the port can read and write the wiki")
	}

	webAuth := false
	switch {
	case cfg.Web.ProxyHeader != "":
		add("info", "web", "identity from proxy header "+cfg.Web.ProxyHeader)
		webAuth = true
	case len(cfg.Web.Users) > 0:
		add("info", "web", fmt.Sprintf("built-in login (%d user(s))", len(cfg.Web.Users)))
		webAuth = true
	default:
		add("info", "web", "open; trusts local access on a loopback bind")
	}
	if webAuth && !mcpTokened {
		add("fail", "posture", "the web UI requires login but the MCP endpoint is open; add .waqwaq/tokens.json")
	}

	if h, e := st.Health(); e == nil {
		if n := len(h.Broken); n > 0 {
			add("warn", "links", fmt.Sprintf("%d broken link(s) (see the Canopy, or waqwaq health tools)", n))
		} else {
			add("ok", "links", "no broken links")
		}
		add("info", "orphans", fmt.Sprintf("%d page(s) nothing links to", len(h.Orphans)))
		if n := len(h.Stale); n > 0 {
			add("info", "stale", fmt.Sprintf("%d page(s) untouched 90+ days", n))
		}
	}
	return out
}

func reportDiags(out []diag) int {
	sym := map[string]string{"ok": "✓", "warn": "⚠", "fail": "✗", "info": "·"}
	warns, fails := 0, 0
	for _, d := range out {
		fmt.Printf("  %s %-9s %s\n", sym[d.level], d.label, d.msg)
		switch d.level {
		case "warn":
			warns++
		case "fail":
			fails++
		}
	}
	fmt.Printf("\n%d problem(s), %d warning(s)\n", fails, warns)
	return fails
}

type checkFinding struct {
	Slug     string `json:"slug,omitempty"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// runCheck lints every page and reports broken links, orphans, and stale pages.
// It uses the resolved link graph for broken links (so basename and piped links
// resolve), not lint's exact-match wikilink warning, which it drops.
func runCheck(st *store.Store, rules lint.Rules) []checkFinding {
	var out []checkFinding
	metas, err := st.List()
	if err != nil {
		return out
	}
	known := make(map[string]bool, len(metas))
	for _, m := range metas {
		known[m.Slug] = true
	}
	for _, m := range metas {
		page, err := st.Read(m.Slug)
		if err != nil {
			continue
		}
		for _, is := range lint.Check(page.Frontmatter, page.Body, known, rules) {
			// Skip lint's exact-match wikilink warning (the graph resolves links)
			// and the missing-frontmatter-title error (a page's title falls back to
			// its H1 or filename, which every read surface uses).
			if strings.HasPrefix(is.Message, "wikilink ") || strings.Contains(is.Message, "missing a non-empty `title`") {
				continue
			}
			out = append(out, checkFinding{m.Slug, is.Severity, is.Message})
		}
	}
	if h, err := st.Health(); err == nil {
		for _, b := range h.Broken {
			out = append(out, checkFinding{b.From, "error", "broken wikilink [[" + b.To + "]]"})
		}
		for _, o := range h.Orphans {
			out = append(out, checkFinding{o.Slug, "warning", "orphan, nothing links here"})
		}
		for _, sp := range h.Stale {
			out = append(out, checkFinding{sp.Slug, "warning", fmt.Sprintf("stale, untouched %d days", sp.Days)})
		}
	}
	if len(metas) == 0 {
		out = append(out, checkFinding{"", "error", "no markdown pages found"})
	}
	return out
}

func cmdCheck(args []string) {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit findings as JSON")
	strict := fs.Bool("strict", false, "treat warnings (orphans, stale) as failures too")
	rest := parseArgs(fs, args)
	dir := "."
	if len(rest) > 0 {
		dir = rest[0]
	}
	st, err := store.New(dir)
	if err != nil {
		log.Fatalf("check: %v", err)
	}
	cfg, _ := config.Load(filepath.Join(st.Root(), ".waqwaq", "config.json"))
	findings := runCheck(st, cfg.Lint)

	errs, warns := 0, 0
	for _, f := range findings {
		if f.Severity == "error" {
			errs++
		} else {
			warns++
		}
	}
	ok := errs == 0 && (!*strict || warns == 0)
	if *asJSON {
		printJSON(map[string]any{"errors": errs, "warnings": warns, "ok": ok, "findings": findings})
	} else {
		for _, f := range findings {
			loc := f.Slug
			if loc == "" {
				loc = "(wiki)"
			}
			fmt.Printf("  %-7s %-26s %s\n", f.Severity, loc, f.Message)
		}
		if len(findings) == 0 {
			fmt.Println("  no problems")
		}
		fmt.Printf("\n%d error(s), %d warning(s)\n", errs, warns)
	}
	if !ok {
		os.Exit(1)
	}
}

func cmdScan(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	rest := parseArgs(fs, args)
	if len(rest) < 2 {
		log.Fatal("usage: waqwaq scan <repo> <outdir>")
	}
	repo, out := rest[0], rest[1]
	n, err := ingest.Go(repo, out)
	if err != nil {
		log.Fatalf("scan: %v", err)
	}
	fmt.Printf("Scanned %d packages into %s\n", n, filepath.Join(out, "wiki"))
	fmt.Printf("Next: waqwaq tui %s   (or  waqwaq serve %s)\n", out, out)
}

func cmdTUI(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	remote, token := remoteFlags(fs)
	rest := parseArgs(fs, args)
	base, err := kbFor(*remote, *token, argAt(rest, 0))
	if err != nil {
		log.Fatalf("tui: %v", err)
	}
	if err := tui.Run(base); err != nil {
		log.Fatalf("tui: %v", err)
	}
}

func cmdTOC(args []string) {
	fs := flag.NewFlagSet("toc", flag.ExitOnError)
	remote, token := remoteFlags(fs)
	asJSON := fs.Bool("json", false, "emit JSON")
	rest := parseArgs(fs, args)
	base, err := kbFor(*remote, *token, argAt(rest, 0))
	if err != nil {
		log.Fatalf("toc: %v", err)
	}
	metas, err := base.List()
	if err != nil {
		log.Fatalf("toc: %v", err)
	}
	if *asJSON {
		printJSON(refsJSON(metas))
		return
	}
	for _, m := range metas {
		fmt.Printf("%s\t%s\n", m.Slug, m.Title)
	}
}

func cmdGrep(args []string) {
	fs := flag.NewFlagSet("grep", flag.ExitOnError)
	remote, token := remoteFlags(fs)
	asJSON := fs.Bool("json", false, "emit JSON")
	tag := fs.String("tag", "", "only pages carrying this frontmatter tag")
	linksTo := fs.String("links-to", "", "only pages that link to this slug")
	rest := parseArgs(fs, args)
	if len(rest) == 0 {
		log.Fatal("usage: waqwaq grep [flags] <query> [dir]")
	}
	base, err := kbFor(*remote, *token, argAt(rest, 1))
	if err != nil {
		log.Fatalf("grep: %v", err)
	}
	hits, err := base.Search(rest[0])
	if err != nil {
		log.Fatalf("grep: %v", err)
	}
	if scope := scopeSet(base, *tag, *linksTo); scope != nil {
		kept := hits[:0]
		for _, h := range hits {
			if scope[h.Slug] {
				kept = append(kept, h)
			}
		}
		hits = kept
	}
	if *asJSON {
		printJSON(hits)
		return
	}
	for _, h := range hits {
		fmt.Printf("%s\t%s\n", h.Slug, strings.TrimSpace(h.Snippet))
	}
}

// scopeSet is the set of slugs allowed by --tag and --links-to (their
// intersection), or nil when neither is set. This is graph-aware scoping plain
// grep cannot express ("search only pages that link to X").
func scopeSet(base kb.KnowledgeBase, tag, linksTo string) map[string]bool {
	if tag == "" && linksTo == "" {
		return nil
	}
	set := map[string]bool{}
	first := true
	narrow := func(slugs []string) {
		next := map[string]bool{}
		for _, s := range slugs {
			if first || set[s] {
				next[s] = true
			}
		}
		set, first = next, false
	}
	if tag != "" {
		tags, err := base.Tags()
		if err != nil {
			log.Fatalf("grep: %v", err)
		}
		narrow(slugsOf(tags[tag]))
	}
	if linksTo != "" {
		in, err := base.Backlinks(linksTo)
		if err != nil {
			log.Fatalf("grep: %v", err)
		}
		narrow(slugsOf(in))
	}
	return set
}

func slugsOf(metas []store.PageMeta) []string {
	out := make([]string, len(metas))
	for i, m := range metas {
		out[i] = m.Slug
	}
	return out
}

func cmdCat(args []string) {
	fs := flag.NewFlagSet("cat", flag.ExitOnError)
	remote, token := remoteFlags(fs)
	doRender := fs.Bool("render", false, "render the markdown for the terminal")
	rest := parseArgs(fs, args)
	if len(rest) == 0 {
		log.Fatal("usage: waqwaq cat [flags] <slug> [dir]")
	}
	base, err := kbFor(*remote, *token, argAt(rest, 1))
	if err != nil {
		log.Fatalf("cat: %v", err)
	}
	slug := rest[0]
	page, err := base.Read(slug)
	if err != nil {
		if canon, ok := base.ResolveLink(slug); ok {
			page, err = base.Read(canon)
		}
	}
	if err != nil {
		log.Fatalf("cat: %v", err)
	}
	if *doRender {
		if out, rerr := glamour.Render(page.Body, "auto"); rerr == nil {
			fmt.Print(out)
			return
		}
	}
	if page.Raw != "" {
		fmt.Print(page.Raw)
	} else {
		fmt.Print(page.Body)
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
