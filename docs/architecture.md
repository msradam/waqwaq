# Architecture

This describes how waqwaq is built and why. For decision history and the full
record of what was cut from earlier versions, see
[BASELINE-NOTES.md](../BASELINE-NOTES.md). For the MCP surface, see
[docs/mcp.md](mcp.md).

## Context and goals

waqwaq serves a single directory of OKF markdown to three read-only surfaces at
once — MCP (for agents), a web view, and a terminal reader — without those
surfaces disagreeing. It is deliberately opinionated: it is an OKF server, and it
enforces the format rather than degrading into a generic markdown server.

Non-goals: writing (there is no mutation path on any surface), authentication,
theming, and a standalone JSON API. Those were cut to keep the baseline small;
MCP is the machine-readable surface.

## The thesis

A single git-backed directory of OKF markdown is the source of truth. Every
surface resolves to the same operations over the same files, so they cannot
disagree — there is one `Core`, and all three surfaces call it.

```
                    ┌──────────────┐
   agents  ───────▶ │  MCP server  │ ─┐
                    └──────────────┘  │
                    ┌──────────────┐  │   ┌────────────┐   ┌──────────────┐
   browsers ──────▶ │  web server  │ ─┼─▶ │  core.Core │─▶ │ files + git  │
                    └──────────────┘  │   └────────────┘   └──────────────┘
                    ┌──────────────┐  │
   terminal ──────▶ │  TUI reader  │ ─┘
                    └──────────────┘
```

## The Core interface

`core.Core` is the whole contract. It is read-only by construction — there is no
`Write` or `Delete`, not even stubbed.

```go
type Core interface {
    List(ctx, prefix, typeFilter, tagFilter string, limit, offset int) (ListResult, error)
    Read(ctx, slug string) (Page, error)
    Search(ctx, query string, isRegex bool, typeFilter, tagFilter string, limit int) ([]Match, error)
    Backlinks(ctx, slug string) ([]string, error)
    Neighbors(ctx, slug string, depth int) ([]GraphNode, error)
    Tags(ctx, tag *string) ([]TagCount, error)
    Hubs(ctx, limit int) ([]GraphNode, error)
    Recent(ctx, limit int) ([]PageMeta, error)
    History(ctx, slug string, limit int) ([]Commit, error)
    Graph(ctx) (Graph, error)
}
```

It is a public, importable package (`github.com/msradam/waqwaq/core`):

```go
c, _ := core.New("/path/to/okf-bundle", true, core.WithRefreshInterval(0))
```

The second argument is OKF enforcement (reject a concept page missing `type` on
read). `core.Validate(ctx, c, strict)` returns a compliance `Report`, and
`core.IsGitRepo(dir)` reports whether history is available.

## Result is stateless; work is cached

Every call reflects the current filesystem — no surface ever sees a stale answer
another surface can't. But the implementation does not re-parse the tree on every
call. Three cache layers, all keyed off a cheap stat-only walk:

1. **Per-file parse cache**, keyed by `(mtime, size)`. An unchanged page is never
   re-read or re-parsed; a modified page re-parses alone.
2. **Derived cache**, keyed by an order-independent corpus signature (a hash of
   every page's `slug + mtime + size`). The concept list, link graph, reverse-link
   index, degrees, and tag index rebuild only when a page is added, removed, or
   modified. All surfaces share this one derived view.
3. **Refresh window** (`WithRefreshInterval`, default 1s; 0 disables it). Within
   the window a read returns the cached view without touching the filesystem, so a
   burst of requests costs one walk, not one per request. Changes are reflected
   within the interval.

`serve` warms the cache in a background goroutine so the first request on a large
wiki does not pay the initial parse. At 100k pages a warm `List` or `Read` is
well under a millisecond; the one-time cold parse is a few seconds. See
[BASELINE-NOTES](../BASELINE-NOTES.md) for the benchmark table.

Because the cache is shared and derived purely from the files, concurrent reads
from different surfaces never disagree — the statelessness thesis holds while the
server scales.

## Data flow: reading a page

1. A surface calls `Core.Read(ctx, slug)`.
2. Read reads that one file from disk (the body is never cached).
3. It calls `snapshot()`, which returns the shared derived graph (a cache hit
   within the refresh window; otherwise a stat-walk that reuses unchanged
   per-file parses).
4. Outbound edges come from the page's parsed links; inbound edges come from the
   reverse-link index — both O(degree), each carrying the neighbour's title and
   OKF type.
5. The surface renders: MCP returns structured JSON, the web view renders HTML +
   a links sidebar, the TUI renders with Glamour and a related-pages footer.

## OKF specifics

- **Link model**: the resolver parses both `[[wikilinks]]` and relative `.md`
  markdown links (`[Orders](../tables/orders.md)`), because real upstream OKF
  bundles link concepts with relative paths. `resource` frontmatter is an external
  deep-link, never a graph edge.
- **Reserved files**: `index.md` and `log.md` are navigation, not concepts. They
  are excluded from the concept catalog (`List`, `wiki_list`, `resources/list`)
  but remain readable and contribute graph edges. `index.md` is the traversal
  entry point.
- **Titles** fall back to a humanized slug when frontmatter omits `title`.
- **Enforcement**: OKF compliance (a `type` on every concept page) is the default.
  `serve`/`mcp` run `core.Validate` at startup and refuse to serve a
  non-compliant bundle; `--lenient` / `"lenient": true` opts out. Broken links are
  reported but tolerated (the spec forbids rejecting a bundle for them).

## Dependencies and constraints

Pure Go, no cgo, cross-compiles with plain `go build` (CI builds with
`CGO_ENABLED=0`). No shelling out to external binaries.

- **git**: `github.com/go-git/go-git/v5`, in-process — no `git` binary needed.
- **search**: a small internal package (`internal/search`) — parallel walk,
  literal keyword-AND + RE2 regex, NUL-byte binary skip, dot-dir skip. No SQLite.
- **markdown**: goldmark (GFM, wikilinks, callouts, client-side Mermaid), Glamour
  for the terminal.
- **frontmatter**: `github.com/adrg/frontmatter` + `gopkg.in/yaml.v3`.

## Package layout

```
core/               public library: the Core interface, caching, git, validation
internal/search/    ripgrep-style scanner
internal/mcpserver/ MCP resources + read-only tools over Core
internal/server/    web view over Core (+ mounts the MCP handler)
internal/tui/       terminal reader over Core
internal/render/    markdown → HTML + table of contents
internal/config/    .waqwaq/config.json
main.go             CLI: serve, mcp, tui, validate, doctor, toc, recent, grep, cat, init
hack/genokf/        generator for large synthetic OKF wikis (scale testing)
perf/               ocarina rondos (deterministic MCP checks / load test)
```

## Key trade-offs

- **Cache vs. staleness.** The refresh window trades up to one interval of
  staleness for O(1) reads under load. Set `WithRefreshInterval(0)` for
  zero-staleness at the cost of a stat-walk per call.
- **Body not cached.** `Read` always reads the page file, so bodies are never
  stale and memory stays bounded; only metadata and the graph are cached.
- **Enforce OKF vs. serve anything.** Enforcing the format is the product stance;
  `--lenient` is the escape hatch for plain markdown.
- **`wiki_graph` returns the whole graph.** It is O(output); on huge wikis agents
  should prefer `wiki_hubs` / `wiki_neighbors` for bounded traversal.

See [BASELINE-NOTES.md](../BASELINE-NOTES.md) for the reasoning behind each of
these and the full list of features cut from earlier versions.
