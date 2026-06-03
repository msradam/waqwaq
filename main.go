package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/msradam/waqwaq/internal/mcpserver"
	"github.com/msradam/waqwaq/internal/render"
	"github.com/msradam/waqwaq/internal/server"
	"github.com/msradam/waqwaq/internal/store"
)

const version = "0.1.0"

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
  waqwaq serve  [dir] [--addr] [--read-only]   serve web UI + MCP over one port
  waqwaq ingest <dir> <file>...       add raw documents to the wiki's raw/ area
  waqwaq version

Pages are served from <dir>/wiki if present, otherwise from <dir> itself, so a
bare markdown folder or an Obsidian vault works without restructuring.

`)
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8000", "address to listen on")
	readOnly := fs.Bool("read-only", envBool("WAQWAQ_READ_ONLY"), "disable writes (AI and human)")
	rest := parseArgs(fs, args)

	dir := "."
	if len(rest) > 0 {
		dir = rest[0]
	}

	st, err := store.New(dir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	mcpSrv := mcpserver.New(st, *readOnly)
	srv, err := server.New(st, render.New(), mcpSrv)
	if err != nil {
		log.Fatalf("server: %v", err)
	}

	httpSrv := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	mode := "read-write"
	if *readOnly {
		mode = "read-only"
	}
	log.Printf("waqwaq %s  ·  %s  ·  %s  ·  pages from %s", version, st.Root(), mode, st.Layout())
	log.Printf("  web UI : http://%s/", *addr)
	log.Printf("  MCP    : http://%s/mcp   (streamable HTTP, add this URL to your MCP client)", *addr)

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
`
