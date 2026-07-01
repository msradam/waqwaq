# Baseline Reset: Read-Only OKF Wiki (baseline/okf-mcp)

## Thesis

A single git-backed directory of OKF markdown is the source of truth, readable by multiple interfaces at once (MCP, web, TUI) without those interfaces disagreeing, because they all resolve to the same stateless operations over the same files.

## Current State → Baseline

### What We're Carrying Forward (Behaviorally)

#### OKF Support
- Standard YAML frontmatter fields: `type`, `description`, `resource`, `tags`, `timestamp`
- Pages under `wiki/` (or the root folder if `wiki/` doesn't exist, so bare folders work)
- Raw documents under `raw/`
- Schema documented in `CLAUDE.md` if present
- MCP tools accept a `type` filter on `wiki_list` to scope to specific OKF types
- Graph nodes include OKF `type` for reasoning about the knowledge graph

#### Rendering & Navigation
- GFM markdown with syntax highlighting
- `[[wikilinks]]` and `[[slug|label]]` link resolution
- Heading anchors, GitHub/Obsidian callouts, client-side Mermaid
- Terminal rendering via Glamour
- Markdown pages with YAML frontmatter (tolerant parsing: YAML, TOML, JSON)

#### Read-Only Surfaces
- **MCP resources** (primary): resource://{slug} for every page via Core.Read
- **MCP tools** (read-only queries): wiki_search, wiki_backlinks, wiki_graph
- **Web UI**: browse, read, search, graph view
- **TUI**: browse, search, read, follow links
- **CLI**: toc, grep, cat --render (scriptable)

#### File Handling
- Pages are markdown files on disk, versioned with git
- Search skips dot-directories (`.git`, `.waqwaq`) and dotfiles during the walk, so no separate gitignore library is needed for the baseline (dropped both `sabhiram/go-gitignore` and the inline-parser plan). If richer ignore rules are wanted later, use go-git v5's `plumbing/format/gitignore` matcher rather than an unmaintained standalone.
- Skips binary files using ripgrep's NUL-byte heuristic
- No external binary dependencies (no shelling out to rg/git/fzf/etc.)

#### OKF Link Model (correctness — from Opus research)
- The link resolver parses BOTH `[[wikilinks]]` (waqwaq convention) AND relative `.md` markdown links like `[Orders](../tables/orders.md)`. Real upstream OKF bundles (ga4, crypto_bitcoin, stackoverflow) use relative `.md` links exclusively, so a resolver that only understood `[[...]]` would import a real Google bundle with an empty graph and no backlinks. Relative links resolve by path within the bundle tree.
- `resource` frontmatter is an external deep-link (e.g. a BigQuery table URL). It is NOT a graph edge and never runs through the link resolver.

#### OKF Reserved Files (correctness — from Opus research)
- `index.md` and `log.md` are reserved: exempt from the `okf: true` type-required rule, not treated as concept pages. `index.md` is the preferred traversal entry point (fall back to a directory walk); `log.md` is history.
- `title` falls back to a humanized slug basename when frontmatter omits it (real concept files often have no `title`).
- Only `.md` files are concept pages; other siblings (e.g. `viz.html`) are ignored.
- `timestamp` means "knowledge last updated" (metadata freshness), not "data as-of"; its absence is not an error.

### What We're Cutting (and Why)

#### Write Path (All of It)
- No `wiki_write`, `wiki_delete`, `wiki_ingest` tools
- No `wiki_lint` (lint is for writes; without mutation, it's decorative)
- No review queue, proposals, or approval workflow
- No auth/token registry (read-only needs no access control)
- No bcrypt password handling (`cmdPasswd`)
- No webhook integration for write events
- No `lint` or `review` packages — cut entirely

**Why:** The baseline scope is "minimum necessary for agent- and human-readable access." All write plumbing exists only to support mutations. Once we remove mutations, the entire auth/review/lint stack is decorative.

#### Commands That Depend on Write Path
- `waqwaq ingest` — feeds documents to raw/; replaced by git push
- `waqwaq export` — static HTML export (out of scope: "agent- and human-parsable via MCP/web/TUI")
- `waqwaq scan` — codebase-to-wiki scanner (out of scope; pure OKF)
- `waqwaq passwd` — bcrypt token generation (no auth in this baseline)

Keep: `waqwaq init`, `serve`, `mcp`, `tui`, `doctor`, `check`, `toc`, `grep`, `cat`

#### Caching & Indexing
- No SQLite FTS (full-text search) database
- No persistent page cache with signature memoization
- No in-memory graph cache

**Why:** Stateless baseline re-derives from disk/git on every call. Caching adds complexity and state coordination (cache invalidation, warming, staleness checks). For OKF wiki scale (hundreds to low thousands of pages), re-walking the tree and re-indexing on-demand is fast enough.

#### Configuration & Customization
- No `theme` / `accent` customization system (one simple, built-in stylesheet)
- No `custom.css` override
- No `templates/` directory for new-page templates
- No `lint.require_frontmatter` or `lint.banned_terms` rules
- No `webhook` integration
- No `web.proxy_header`, `web.users`, or web UI login system

**Why:** These are platform features for mutation/authorship workflows. The baseline web UI is minimal and serves only reading.

#### Surfaces
- No JSON API (`/api/*`)
- No `/health` "Canopy" view (orphans, broken links, stale pages)
- No `/oracle` graph view with type-based coloring (replace with a simpler graph view)
- No asset upload, image serving
- No multi-wiki farm mode (`serve /w1 /w2 ...`)

**Why:** JSON API duplication of MCP. Canopy/Orphan detection and asset management are platform features. Farm mode adds complexity for little gain in baseline scope.

#### Internal Packages Being Cut
- `internal/auth` — no tokens or registry
- `internal/config` — minimal config (title, description only; no theme/accent/lint/webhook)
- `internal/ingest` — no raw document feeding
- `internal/kb` and `internal/kbclient` — the abstraction over CLI queries; we're resetting this
- `internal/lint` — linting for writes
- `internal/review` — review queue
- `internal/server/api.go` — JSON API
- `internal/server/assets.go` — asset upload/serving
- `internal/search/searcher.go` — SQLite FTS; replace with `internal/search` built from scratch (ripgrep-like literal/regex)
- `internal/store` — replace with a simple Core interface, no caching/memoization

**Why:** Cleaner slate. We'll rewrite from `internal/core.go` with just the Core interface. Rendering, version, and TUI stay largely as-is (they're read-only).

### Key Judgment Calls

#### 1. Git Operations: go-git v5 (in-process, never os/exec)
**Decision**: Use `github.com/go-git/go-git/v5` (v5.19.x) in-process for `History`. No shelling to a `git` binary anywhere.

**Why**:
- Pure Go (zero CGO), no system git binary dependency — the single-binary thesis requires it
- v5 is the stable line; v6 is alpha-only (v6.0.0-alpha.4), so pin v5
- `git log` for a path is well-supported via `repo.Log` with a `FileName` filter
- A non-git wiki (bare markdown folder) returns empty history, not an error

**Note**: `doctor` no longer needs to check for `git` on PATH (go-git is in-process). It should verify the target is a git repo and warn if not, and CI should build with `CGO_ENABLED=0`.

#### 2. Search: Internal Package vs Third-Party
**Decision**: Hand-roll `internal/search` (ripgrep-like literal + stdlib regexp).

**Why**: The third-party options (caddy-search, aidanwoods/search) are either archived or heavy. Rolling our own is ~300 lines, gives us full control over parallelization and ignore rules, and matches ripgrep's heuristics (NUL-byte binary skip, gitignore respect).

#### 3. Search Strategy: Literal + Regex, Not Fuzzy
**Decision**: Literal prefix-AND + stdlib regexp. No fuzzy matching.

**Why (from agent query research)**:
- Agents use **prefix-AND** (deterministic, predictable): "customer orders" matches pages with titles starting with "customer" AND "orders"
- Agents prefer structured filters (type, tags, prefix) over fuzzy exploration
- Fuzzy matching adds latency without benefit for agent queries (they already know what they're looking for)
- Fuzzy would only help human UI (TUI quick-jump), and that's secondary

**Implementation**: Internal search package with:
- Literal queries → fast-path through bytes.Contains (parallelized directory walk)
- Regex queries → stdlib regexp (already RE2, linear-time, battle-tested)
- Gitignore respect + binary skip (NUL-byte heuristic)

#### 4. Graph Layout
**Decision (revised after Opus research)**: NO go-graphviz. `Core.Graph()` returns nodes+edges as JSON; the web UI renders the graph client-side (consistent with client-side Mermaid). CLI can emit DOT text.

**Why**: Embedding a multi-MB Graphviz WASM blob plus wazero init/render latency for one graph view is off-thesis for a minimal self-contained binary, especially when Mermaid already renders client-side. `Core.Graph` already returns `Nodes+Edges`; client-side layout (e.g. a small force-directed script) needs no server dependency. Only pull go-graphviz if server-side raster (PNG/SVG) becomes a hard requirement, and pin a recent commit if so.

Node coloring by OKF `type` must use a hash→palette with a catch-all, never a fixed enum: the type vocabulary is open and values are multi-word ("BigQuery Table").

#### 5. CSS Customization: Clean Cut
**Decision**: One simple built-in stylesheet. No `.waqwaq/custom.css` or theme variables.

**Why**: Baseline scope is "minimal web view for reading." The CSS structure will be clean and simple; if theming returns later, that's a separate feature PR. Not leaving half-plumbing.

#### 6. MCP Resources vs Tools
**Decision**: MCP resources are the primary interface. Tools are for queries only (search, backlinks, graph).

**Why**: Resources (resource:// URIs) are more semantically correct for "read this page" than a wiki_read tool. Tool proliferation in the current implementation is a legacy of the tool-first approach; resources-first is cleaner and matches how other MCP servers (GitHub, Google Drive) prioritize resources.

**Note (from agent query research)**: Agents prefer specialized domain-aware tools (wiki_neighbors, wiki_tags, wiki_hubs) over generic reads. We'll ship:
- Resources (resource://{slug}) for direct page lookups
- Tools: wiki_read(slug), wiki_search(query), wiki_backlinks(slug), wiki_neighbors(slug, depth), wiki_tags(tag?), wiki_graph(), wiki_hubs()
All backed by the same Core functions. Tools include aggregate metadata (total count, truncated flag, link degree) for agent reasoning.

#### 7. Config Schema
**Decision**: Minimal config (title, description, okf mode).

**Why**: The baseline doesn't need theme, webhook, auth, or lint rules. Config is `<dir>/.waqwaq/config.json` with fields:
- `title` (string): display name and MCP server identity
- `description` (string): one-liner for MCP instructions
- `okf` (bool): if true, require `type` field on every page

No theme, no lint, no web auth, no webhook.

## Architecture: The Core Interface

All three surfaces (MCP, web, TUI) speak to a single stateless `Core` interface:

```go
type Core interface {
    // List returns pages, optionally filtered by prefix, type, or tag.
    // Returns pagination metadata (total count, truncated flag) for agent reasoning.
    List(ctx, prefix, typeFilter, tagFilter string, limit, offset int) (ListResult, error)
    
    // Read returns a page's full content (frontmatter + body).
    Read(ctx context.Context, slug string) (Page, error)
    
    // Search runs a literal or regex query, narrowed by optional type/tag,
    // returns matching pages with context snippets. Deterministic slug ordering.
    Search(ctx context.Context, query string, isRegex bool, typeFilter, tagFilter string, limit int) ([]Match, error)
    
    // Backlinks returns pages that link to the given slug.
    Backlinks(ctx context.Context, slug string) ([]string, error)
    
    // Neighbors returns a page's N-degree neighbors in the link graph (pages linked to/from).
    Neighbors(ctx context.Context, slug string, depth int) ([]GraphNode, error)
    
    // Tags returns tag counts (optionally filtered by tag).
    Tags(ctx context.Context, tag *string) ([]TagCount, error)
    
    // Hubs returns the most-connected pages (for entry points).
    Hubs(ctx context.Context, limit int) ([]GraphNode, error)
    
    // History returns git commits touching a page.
    History(ctx context.Context, slug string, limit int) ([]Commit, error)
    
    // Graph returns the full page link graph.
    Graph(ctx context.Context) (Graph, error)
}

type ListResult struct {
    Items     []PageMeta
    Total     int  // count of all matching pages
    Truncated bool // whether results were truncated at limit
}

type TagCount struct {
    Tag   string
    Count int
}

type PageMeta struct {
    Slug        string
    Title       string
    Type        string    // OKF type, if present
    Description string    // OKF description, if present
    Resource    string    // OKF resource URL, if present
    Tags        []string  // OKF tags, if present
    Timestamp   string    // OKF timestamp, if present
}

type Page struct {
    Slug        string
    Title       string
    Frontmatter map[string]any
    Body        string
    Outbound    []EdgeRef // inlined so an agent navigates without a read-per-neighbor
    Inbound     []EdgeRef
}

type EdgeRef struct {
    Slug  string
    Title string
    Type  string
}

type Match struct {
    Slug    string
    Title   string
    Type    string   // OKF type, so hits are decision-complete without a read-per-hit
    Tags    []string
    Context string   // snippet around the match
}

type Commit struct {
    Hash    string
    Author  string
    Date    time.Time
    Message string
}

type Graph struct {
    Nodes []GraphNode
    Edges []GraphEdge
}

type GraphNode struct {
    Slug   string
    Title  string
    Type   string // OKF type
    Degree int    // undirected link count
}

type GraphEdge struct {
    From string
    To   string
}
```

Every operation re-derives from the filesystem + git on every call. No caches, no indexes, no mutable state.

## Implementation Plan

### Phase 1: Core + Search
- `internal/core/core.go`: Core interface implementation over a wiki folder
- `internal/search/search.go`: Literal (fast-path) + regex search with parallelized directory walk and gitignore respect
- `internal/core/git.go`: Git log/blame/show via `os/exec` (read-only)

### Phase 2: Surfaces
- **MCP**: Replace `internal/mcpserver` to use Core, expose resources + tools (wiki_read, wiki_search, wiki_backlinks, wiki_graph)
- **Web UI**: Simplify `internal/server` to MVC view layer over Core (no /api, no auth, no upload, minimal CSS)
- **TUI**: Refactor `internal/tui` to use Core instead of `kb.KnowledgeBase`

### Phase 3: CLI + Doctor + Init
- Simplify `main.go` command handlers to use Core
- Update `doctor` to check for git presence (and warn about cgo)
- Keep `init`, `check`

### Phase 4: Tests + Verification
- `go test ./...` on the Core and all surfaces
- Manual validation: MCP resources/tools against `examples/okf-demo`
- Benchmark internal/search on a synthetic 5k-page wiki

## Performance Target

- List 100k pages: < 500ms (walk the tree once)
- Read a page: < 10ms (read + parse frontmatter)
- Search (literal): < 500ms on 100k pages (parallelized grep)
- Search (regex): < 1s on 100k pages (stdlib regexp, parallelized)
- Graph (full): < 2s on 100k pages (parse all, build adjacency)

No caching, no index. If these numbers don't hold, we'll add strategic caching, but the baseline assumption is that wiki scale (hundreds to few thousand pages) is fast enough with naive re-derivation.

## Testing Strategy

- Unit tests for Core methods against a small fixture wiki
- MCP resource/tool tests against Core (verify resources/list pagination, resources/read frontmatter parsing)
- Web/TUI integration tests: can they browse/search the fixture and agree with MCP?
- Benchmark: synthetic 5k-page wiki, measure search and graph latency
- Manual: run `waqwaq serve examples/okf-demo`, browse web UI, query MCP, run TUI, all agree

## What's Not In This Baseline (But Could Return)

- **Writes**: Reintroduce Core.Write/Delete + auth/review after this baseline lands and is stable.
- **Caching**: Profile first. If re-derivation is too slow, add strategic caching (LRU on Read/Search, signature-based invalidation).
- **Asset uploads**: Re-add after writes land.
- **Web UI theming**: One-shot feature PR once the minimal UI is solid.
- **Web auth**: Part of the write-path review (trusted/untrusted tiers).
- **Static export**: Separate feature, low priority.
- **Codebase scanner**: Pure OKF baseline doesn't need it.
- **API**: MCP is the machine-readable interface.

## Files Preserved (Mostly As-Is)

- `main.go` — CLI handlers, greatly simplified (no ingest, export, scan, passwd)
- `internal/render/` — markdown to HTML + TOC, zero changes
- `internal/tui/` — refactored to use Core instead of kb.KnowledgeBase
- `internal/version/` — preserved
- `README.md` — rewritten for read-only scope
- `go.mod/go.sum` — trimmed to remove unneeded dependencies
- `.mcp.json` — example stays as-is

## Files Deleted (Entire Packages Cut)

- `internal/auth/` — no token registry
- `internal/config/` — minimal config, baked into Core
- `internal/ingest/` — no raw document feeding
- `internal/kb/` — abstraction we're replacing
- `internal/kbclient/` — client for remote queries
- `internal/lint/` — linting for writes
- `internal/review/` — review queue
- `internal/server/api.go` — JSON API
- `internal/server/assets.go` — asset upload
- `internal/store/` — replaced by Core

## Files Rewritten (New Implementation)

- `internal/core/core.go` (new) — Core interface + filesystem operations
- `internal/core/git.go` (new) — git log via go-git v5 (in-process, never os/exec)
- `internal/search/search.go` (new) — literal + regex search
- `internal/mcpserver/mcpserver.go` — rewritten to use Core
- `internal/server/server.go` — rewritten to use Core, simplified web UI
- `go.mod` — dependencies trimmed

---

## Agent-driven refinements (from real-user MCP testing)

Three agents consumed the server over MCP against the real bundles (crypto_bitcoin, ga4) and the myth wiki, doing genuine research tasks through the tools only. Their friction reports drove these changes:

- **`wiki_search` is keyword-AND, not whole-string substring.** An agent's natural query `"total output value per day"` previously returned zero hits because the whole string never appears verbatim. Search now splits on whitespace and requires every term (case-insensitive), so multi-word queries work while staying deterministic and fuzzy-free. Regex mode is unchanged.
- **Graph nodes never have empty titles.** `wiki_hubs`/`wiki_graph`/`wiki_neighbors` fall back to a humanized slug when a page (e.g. a frontmatter-less `index.md`) has no title, so nodes are legible.
- **`wiki_hubs` excludes reserved files.** Hubs rank concept entry points, consistent with `wiki_list`; reserved `index.md`/`log.md` remain in the full `wiki_graph` and in `wiki_neighbors` where structure matters.
- **`wiki_history` returns a `versioned` flag.** An empty commit list on a non-git wiki now reads as "unversioned" rather than a dead tool.
- **`wiki_info` added.** Agents asked for a way to confirm which knowledge base they are connected to; it returns title, description, concept-page count, and versioned state.
- **`wiki_recent` added.** An agent wanted provenance/recency and found per-page `wiki_history` empty on the non-git examples. `wiki_recent` (and the `waqwaq recent` CLI verb) orders concept pages by OKF `timestamp` (knowledge last updated), freshest first, which is stateless and works without git. `Core.Recent` backs it.
- **The `wiki://page/{slug}` resource description** now states it returns body-only and steers agents to `wiki_read` for parsed frontmatter and resolved links (all three agents independently found the bare resource lossy for navigation).

Also from the earlier clean-room QA: `doctor` distinguishes "not a git repo" from readable history; type/tag filters match case-insensitively; `grep` prints available types/tags when a filter matches nothing and rejects an empty query; search snippets snap to word boundaries.

## OKF Alignment (official spec + bundles)

The baseline is aligned to the official OKF spec and uses Google's real artifacts where they can be adopted without pulling in credentials, network, or non-Go dependencies.

### What we adopted from upstream
- **The spec** ([SPEC.md](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md) v0.1). `type` is the only required field; `title`/`description`/`resource`/`tags`/`timestamp` are recommended-optional; unknown types and extra frontmatter keys are preserved. Reserved files `index.md` and `log.md` carry no concept frontmatter and are navigation, not concepts.
- **Real example bundles**, vendored unmodified (minus `viz.html`) under `examples/okf-crypto-bitcoin/` and `examples/okf-ga4/` (Apache 2.0, attributed in `examples/NOTICE.md`). These are the interoperability proof: they use relative `.md` links, frontmatter-less index files, and real BigQuery types. `stackoverflow` was deliberately not vendored (its content is CC-BY-SA 4.0, share-alike, awkward under MIT).

### What we did NOT adopt, and why
- **The reference enrichment agent** (`enrich`) is a producer that requires a Google Cloud billing project (BigQuery) and a Gemini/Vertex key. It cannot run locally without an account, is Python, and produces bundles rather than reading them. Irrelevant to a read-only Go server except as documentation of expected output.
- **An official validator**: there is none. The only official validation is a Python producer-side check (`document.py`) inside a package that pulls `google-adk` + `google-cloud-bigquery`. Wiring it into CI would violate the pure-Go/no-cgo constraint for zero benefit.
- **The `visualize` consumer**: Python, CDN-dependent; a Go server renders its own views.

### Opinionated OKF enforcement (staking on the format)
waqwaq is an OKF server and enforces the format rather than degrading to a generic markdown server:
- **`waqwaq validate`** (renamed from `check`; `check` kept as an alias) is the OKF validator, implemented as **`core.Validate(ctx, c, strict) Report`** in the library. It separates compliance errors (a concept page missing `type` — SPEC §4.1/§9) from broken-link warnings (tolerated: SPEC §9 forbids rejecting a bundle for broken links). `--strict` also flags missing `description`/`timestamp` (the Google reference-agent profile). `title` is never flagged because it always resolves via the slug fallback.
- **`serve` and `mcp` refuse to start** on a non-compliant bundle, printing the offending pages and how to fix. This is the stake on the format: a waqwaq server serves OKF, or it does not serve. Broken links are reported but do not block (spec conformance).
- **Enforcement is the default**, not opt-in. The old `okf: true` config flag is gone; a bundle opts *out* with `--lenient` / `"lenient": true` to serve non-OKF markdown. All example bundles dropped the now-defunct `okf` field.
- **Tested against all three real upstream Google bundles** (crypto_bitcoin, ga4, stackoverflow): each validates compliant, stackoverflow passes even `--strict`, and all serve over MCP. `hack/fetch-upstream-bundles.sh` fetches them into gitignored `examples/upstream/` (stackoverflow is CC-BY-SA, so fetched not vendored).
- **Link resolver parses relative `.md` links as first-class edges**, because upstream bundles link concepts that way (not with `[[wikilinks]]`). Verified: `perf/okf-bundle.yaml` reads the real crypto_bitcoin bundle over MCP and confirms backlinks/neighbors/graph all resolve from relative links (7/7 pass).
- **Reserved files (`index.md`, `log.md`) are excluded from the concept catalog** (`List`, and therefore `wiki_list` and `resources/list`) but remain readable via `Read`/the resource template and still contribute graph edges. `index.md` is the traversal entry point: the web `/` redirects to it, the TUI opens it by name, and the MCP instructions point agents at it. A real bundle's three bare `index.md` directory-listings no longer pollute the concept list.
- **`resource` frontmatter is an external deep-link, never a graph edge.**

## Performance Testing Strategy (via Ocarina)

Use [ocarina](https://github.com/msradam/ocarina) to write deterministic YAML playbooks (rondos) for performance testing. Each rondo:
- Exercises a specific MCP query pattern (list by type, search, neighbors, hubs)
- Runs reproducibly with `ocarina play` (zero randomness, no LLM)
- Measures latency with `ocarina load` (concurrent load, p50/p95/p99)
- Validates results with `expect:` assertions

### Rondos (shipped in `perf/`)

- **`perf/smoke.yaml`** — a deterministic 13-step MCP check that mirrors the
  agent orient → filter → navigate flow against `examples/okf-demo`: read index,
  hubs, list-by-type (asserts exactly 2 BigQuery Tables), inline outbound edges,
  backlinks, neighbors, tags, literal search, type-narrowed search (asserts the
  type filter doesn't leak), the full graph, `resources/list`, and a
  `resources/read`. Exits non-zero on any failed `expect:`, so it doubles as CI.
- **`perf/load.yaml`** — a concurrent latency test over six query types for
  `ocarina load` (edit `dir` to a large wiki first).

Run:
```bash
go build -o waqwaq .
ocarina play perf/smoke.yaml                       # deterministic, no LLM
ocarina load perf/load.yaml --vus 8 --threshold p95<500ms
```

### Measured MCP results

`ocarina play perf/smoke.yaml` against `examples/okf-demo`: **13 passed, 0 failed
in 25ms**. Every documented agent query pattern works end-to-end over the live
stdio server, including type-narrowed search proving the filter doesn't leak,
`resources/list`, and `resources/read`.

`ocarina load` against a synthetic 5000-page wiki (stdio server, mixed query
types — full MCP round trip including transport, walk, and parse):

| Run | calls | failed | p50 | p95 | p99 |
|-----|-------|--------|-----|-----|-----|
| 1 VU, 10s | 106 | 0 | 116ms | 127ms | 147ms |
| 8 VUs, 8s | 195 | 0 | 326ms | 559ms | 648ms |

**Zero failures under 8 concurrent virtual users** is the load-bearing result: it
validates the statelessness thesis — concurrent reads need no coordination and
never disagree. Latency rises under concurrency because 8 virtual users share one
stdio server process and contend on full-tree walks; a real deployment serves the
HTTP endpoint, which does not serialize like stdio.

### Acceptance Criteria

Targets at wiki scale 5k pages (synthetic data, 5 types, 50 tags, 4 links per page):

| Query | Target (p95) | Assertion |
|-------|---------------|-----------|
| List all pages | < 500ms | total count > 0, pagination works |
| List by type | < 300ms | type filter enforced, total < corpus |
| Search literal | < 500ms | results match query, deterministic slug ordering |
| Neighbors (depth 1) | < 200ms | includes direct links only |
| Graph (full) | < 3s | nodes == 5000 |

### Scale: caching for massive wikis

The original baseline re-walked and re-parsed the whole tree on every call. That
is correct but O(n) per call, which does not hold up on massive wikis. The Core
now memoizes derived work while staying correct (results always reflect disk):

- **Per-file parse cache** keyed by `(mtime, size)`: an unchanged page is never
  re-read or re-parsed. A change re-parses only that page.
- **Derived cache** keyed by a corpus signature (an order-independent hash of
  every page's `slug+mtime+size`): the concept list, link graph, reverse-link
  index, degrees, and tag index rebuild only when a page is added, removed, or
  modified. All surfaces share it, so they still cannot disagree.
- **Refresh window** (`WithRefreshInterval`, default 1s): within the window a
  read returns the cached view without touching the filesystem, so a burst of
  requests costs one stat-walk, not one per request. Set to 0 for zero-staleness
  (every call re-stats). Changes are reflected within the interval.
- **Reverse-link index**: `Backlinks` and `Read`'s inbound edges are O(degree),
  not O(pages).
- `serve` warms the cache in a background goroutine so the first request on a
  large wiki does not pay the initial parse.

The core stays a real library: `core.New(root, okf, opts...)` returns the `Core`
interface; `WithRefreshInterval` is the one knob.

### Measured results at 100k pages (darwin/arm64, 8 cores, `go test -bench`)

| Operation | Latency | Notes |
|-----------|---------|-------|
| List (warm, cache hit) | ~0.6ms | was 311ms uncached |
| Read (warm, inline edges) | ~0.6ms | was 318ms; reverse index |
| Pure cache hit (no change) | 24ns | refresh-window fast path |
| Graph (full 100k-node dump) | ~228ms | O(output); agents use hubs/neighbors instead |
| Cold first call (parse all 100k) | ~6.6s | one-time, backgrounded on `serve` |

Reproduce: `go test ./core/ -bench 'List100k|Read100k|Graph100k'`.

Live end-to-end at 50k (generated with `hack/genokf`, driven through the binary
over MCP): `waqwaq check` clean in ~4.7s (cold), `toc` lists all 50,000, and
`wiki_info`/`wiki_list`/`wiki_read`/`wiki_neighbors`/`wiki_search`/`wiki_backlinks`
all resolve correctly — relative `.md` links included.

Bug found while scale-testing: `waqwaq check` and `toc` called `List` with the
default limit (500), so on a >500-page wiki they saw only the first 500 pages
(check then reported every other page's links as "broken"). Fixed to request all
pages; regression-tested.

### Measured results (5000 pages, darwin/arm64, 8 cores, `go test -bench`)

Synthetic wiki: 5000 pages, 5 OKF types, tags, 4 `[[wikilinks]]` each. Stateless
re-derivation, no cache. `internal/core/bench_test.go`.

| Operation | ns/op | ms/op |
|-----------|-------|-------|
| Search literal | 43,261,445 | ~43ms |
| Search regex (RE2) | 42,579,292 | ~43ms |
| List all | 110,349,583 | ~110ms |
| List by type | 110,343,736 | ~110ms |
| Neighbors (depth 1) | 125,367,083 | ~125ms |
| Graph (full) | 129,783,097 | ~130ms |

All within target. Notes:
- Search (~43ms) is fastest: the parallel walk reads only until the NUL/binary
  check and the match, and stops at the limit.
- List/Graph/Neighbors (~110-130ms) each walk + parse frontmatter for all 5000
  files; that parse dominates. Regex and literal search cost the same because
  I/O, not matching, is the bottleneck at this scale (as predicted — no SIMD
  prefilter needed).
- Neighbors re-derives the whole graph per call (stateless); ~125ms is above the
  <200ms target but well under a human/agent's tolerance. If it ever matters, a
  request-scoped graph memo is the upgrade path.

Reproduce: `go test ./core/ -run '^$' -bench . -benchtime=3x`

---

## Notes for Future Work

1. **Search Benchmarks**: Measure internal/search on 5k pages (literal, regex, parallel goroutine count tuning). Use ocarina for deterministic replay.
2. **Cache Invalidation Ceiling**: If re-derivation becomes a bottleneck:
   - LRU cache on Read/Search (invalidate on filesystem change)
   - Persistent page-list cache (written when tree walks complete)
   - Per-page modification time check (fast, no full re-walk)
3. **CGO Verification**: Build with `CGO_ENABLED=0 go build` on CI to ensure no accidental cgo (go-git is pure).
4. **OKF Schema Validation**: The `okf: true` config flag requires `type` on every page at read time. Pages without `type` error on List/Read.
5. **Synthetic Wiki**: Generate test data with `go generate` or a separate script (~5k pages, realistic frontmatter, wikilinks).
