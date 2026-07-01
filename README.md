<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="noun-tree-dark.png">
    <img src="noun-tree-light.png" alt="" width="84">
  </picture>
</p>

# Waqwaq

A single directory of OKF markdown, read at once through MCP, a web view, and a terminal view. Every surface resolves to the same stateless operations over the same files, so they never disagree.

## What it is

Waqwaq serves a git-backed folder of [Open Knowledge Format](https://github.com/GoogleCloudPlatform/knowledge-catalog/tree/main/okf) markdown three ways from one binary: an MCP endpoint for agents, a web view for people, and a terminal reader. OKF bundles are directories of markdown with a small set of YAML frontmatter fields (`type`, `description`, `resource`, `tags`, `timestamp`). Pages under `wiki/` are the source of truth; point waqwaq at a folder with no `wiki/` subdirectory and it serves that folder directly.

waqwaq is opinionated: it is an OKF server, and it enforces the format. It refuses to serve a bundle that is not OKF compliant (every concept page must carry a `type`, per the spec). To serve plain markdown that is not an OKF bundle, opt out with `--lenient` or `"lenient": true` in config.

This is a read-only baseline. There is no write path on any surface: no editing, uploading, or mutation tools. The value it protects is that many readers see one consistent knowledge base with no coordination between them, because the core is stateless and re-derives every answer from the filesystem and git on each call.

## Install

```bash
go install github.com/msradam/waqwaq@latest   # or: go build -o waqwaq .
```

Pure Go, no cgo. Cross-compiles to any `GOOS`/`GOARCH` with plain `go build` (verified with `CGO_ENABLED=0`). Git history is read in-process via go-git, so no `git` binary is required on the host.

## Usage

```bash
waqwaq serve examples/okf-demo          # web view + MCP on one port
```

The web view is at `http://127.0.0.1:8000/`, the MCP endpoint at `/mcp`. Connect an agent with a `.mcp.json`:

```json
{ "mcpServers": { "wiki": { "type": "http", "url": "http://127.0.0.1:8000/mcp" } } }
```

Or, with no running server, as a stdio subprocess:

```json
{ "mcpServers": { "wiki": { "command": "waqwaq", "args": ["mcp", "/path/to/wiki"] } } }
```

Other commands:

```bash
waqwaq tui    <dir>            browse in a terminal reader
waqwaq doctor <dir>            check setup and MCP posture
waqwaq validate <dir> [--json] [--strict]   check OKF compliance and links, for CI (non-zero on problems)
waqwaq toc    <dir>            list pages as slug<tab>title
waqwaq recent <dir> [--limit]  list pages by OKF timestamp, freshest first
waqwaq grep   <query> <dir>    full-text search; --regex, --type, --tag
waqwaq cat    <slug> <dir>     print a page; --render for terminal markdown
waqwaq init   <dir>            scaffold a small OKF wiki
```

## Documentation

- [docs/mcp.md](docs/mcp.md) — MCP reference: resources, every tool, argument and object shapes, the orient/navigate flow.
- [docs/architecture.md](docs/architecture.md) — how it is built: the Core interface, the caching design, data flow, and trade-offs.
- [BASELINE-NOTES.md](BASELINE-NOTES.md) — decision history and what earlier versions had that this deliberately cuts.

## Surfaces

All three read surfaces share one stateless core, so their answers are identical.

- **MCP** (`serve`, or `mcp` over stdio). Resources are the primary read interface: the template `wiki://page/{slug}` reads any page's body, and `resources/list` enumerates every concept page. Tools cover the genuine queries: `wiki_info` (confirm the connected wiki), `wiki_list` (filter by prefix, type, tag; paged with `total`/`truncated`), `wiki_read` (body plus inlined outbound and inbound links, each with title and type), `wiki_search` (keyword AND, or regex, narrowable by type and tag), `wiki_backlinks`, `wiki_neighbors`, `wiki_tags`, `wiki_hubs`, `wiki_recent` (pages by OKF timestamp, freshest first), `wiki_graph`, `wiki_history`. Every tool is annotated read-only; there are no mutation tools. For navigation prefer `wiki_read` over the raw resource: it also returns parsed frontmatter and resolved links.
- **Web view** (`serve`). Browse, read rendered markdown with a table of contents and a links sidebar, search, and a client-side force-directed graph coloured by OKF type. One built-in stylesheet, no configuration.
- **Terminal reader** (`tui`). Browse, fuzzy-filter, full-text search, read rendered pages, and walk a page's links.

`wiki_read` and the `wiki://page/{slug}` resource return the same data; the tool exists for clients without the resources primitive.

## OKF support

Waqwaq reads the OKF frontmatter fields and exposes them across every surface:

- `wiki_list` and `wiki_search` accept a `type` filter, so an agent can ask for all `BigQuery Table` or all `Metric` pages.
- `wiki_read` and the page resource inline each neighbour's slug, title, and type, so an agent navigates without a read per neighbour.
- The graph view colours nodes by type using an open palette, so producer-defined types (any string, often multi-word) render without a fixed vocabulary.
- The link resolver understands both `[[wikilinks]]` and relative `.md` links (`[Orders](../tables/orders.md)`), which is how upstream OKF bundles express edges, so a real Google bundle graphs correctly.
- `resource` frontmatter is treated as an external deep-link to the underlying asset, distinct from links between pages.
- `index.md` and `log.md` are reserved: exempt from the type requirement and not treated as concept pages. `index.md` is the natural entry point.
- OKF compliance (a `type` on every concept page) is enforced by default: `serve` and `mcp` refuse to start on a non-compliant bundle, and `waqwaq validate` reports the offenders.

Waqwaq follows the [OKF specification](https://github.com/GoogleCloudPlatform/knowledge-catalog/blob/main/okf/SPEC.md) v0.1: `type` is the only required field, unknown types and extra frontmatter keys are preserved, and reserved files are treated as navigation rather than concepts.

### Examples

Four OKF bundles ship under `examples/`:

- `okf-crypto-bitcoin/` and `okf-ga4/` are real bundles vendored unmodified from Google's [knowledge-catalog](https://github.com/GoogleCloudPlatform/knowledge-catalog) (Apache 2.0, see [examples/NOTICE.md](examples/NOTICE.md)). They exercise genuine interoperability: concepts linked by relative `.md` paths, frontmatter-less `index.md` directory listings, and `type` values like `BigQuery Table` and `Reference`.
- `okf-demo/` is a small hand-made data catalog used as the deterministic test fixture.
- `waqwaq-myth/` is an OKF bundle in a non-data-catalog domain (the medieval Waq-Waq legend), showing the open type vocabulary (`Legend`, `Place`, `Reference`).

```bash
waqwaq serve examples/okf-crypto-bitcoin   # a real Google OKF bundle
```

### Validating a bundle

`waqwaq validate` checks a bundle against the OKF spec (every non-reserved concept page has a non-empty `type`) and reports broken links:

```bash
waqwaq validate examples/okf-ga4           # spec conformance: type required
waqwaq validate --strict examples/okf-ga4  # also require description, timestamp
```

The default matches the spec (only `type` is required; broken links are warnings, since the spec forbids rejecting a bundle for them). `--strict` also requires the recommended fields Google's reference enrichment agent emits. `serve`/`mcp` run the same compliance check at startup and refuse to serve a non-compliant bundle.

There is no official runnable OKF validator (the reference tooling is Python and needs a Google Cloud account), so this implements the spec's conformance rules directly in Go, exposed as `core.Validate` for library use.

### Testing against upstream bundles

`hack/fetch-upstream-bundles.sh` fetches Google's real OKF bundles (crypto_bitcoin, ga4, stackoverflow) into `examples/upstream/` (gitignored) so you can validate and serve real third-party OKF data:

```bash
./hack/fetch-upstream-bundles.sh
waqwaq validate examples/upstream/stackoverflow
waqwaq serve    examples/upstream/stackoverflow
```

## Scale

The core is stateless in result (every call reflects the current filesystem) but
caches derived work so it holds up on large wikis. It memoizes parsed frontmatter
per file (keyed by mtime and size) and the derived link graph (keyed by a corpus
signature), rebuilding only what changed. A refresh window (default one second)
lets a burst of reads share a single filesystem walk; `serve` warms the cache in
the background so the first request is not cold.

Measured at 100,000 pages (darwin/arm64): a warm `List` or `Read` is well under a
millisecond; the one-time cold parse of the whole corpus is a few seconds and is
backgrounded on `serve`. All surfaces share the one cache, so they never
disagree.

Generate a large OKF wiki to test against:

```bash
go run ./hack/genokf -out /tmp/bigwiki -pages 100000
waqwaq serve /tmp/bigwiki
```

As a library:

```go
import "github.com/msradam/waqwaq/core"

c, _ := core.New("/path/to/okf-bundle", true, core.WithRefreshInterval(0))
pages, _ := c.List(ctx, "", "BigQuery Table", "", 50, 0)
```

`core.Core` is the read-only interface every surface (MCP, web, TUI) resolves to.

## Configuration

Optional settings live in `<dir>/.waqwaq/config.json`, all fields optional:

```json
{
  "title": "Acme Data Catalog",
  "mcp_description": "Acme's internal data catalog: datasets, tables, and metrics",
  "addr": "127.0.0.1:8000"
}
```

- `title`: display name and MCP server identity. An MCP client sees this as the server name, not "waqwaq".
- `mcp_description`: one-liner shown to agents in MCP instructions.
- `addr`: default listen address for `serve` (the `--addr` flag overrides it).
- `lenient`: when `true`, disables OKF enforcement so waqwaq serves plain markdown that is not an OKF bundle. Off by default (waqwaq enforces OKF).

A `CLAUDE.md` at the wiki root, if present, is served to agents as MCP instructions.

## Testing

Deterministic MCP checks run through [ocarina](https://github.com/msradam/ocarina), which replays a YAML playbook against the live server with no model in the loop:

```bash
go build -o waqwaq .
ocarina play perf/smoke.yaml            # 13-step orient/navigate check on okf-demo
ocarina play perf/okf-bundle.yaml       # interop check against the real crypto_bitcoin bundle
ocarina load perf/load.yaml --vus 8 --threshold p95<500ms
```

`perf/smoke.yaml` and `perf/okf-bundle.yaml` double as CI smoke tests and exit non-zero on any failed assertion. Core and surface unit tests run with `go test ./...`.

## Not yet reattached

This baseline deliberately drops features that earlier versions had. They are cut, not hidden, and would each return as their own change:

- **Any write path**: editing, uploading, the review and proposal workflow, webhooks, token and proxy-header auth, roles. All of it existed to support writes. Reattaching writes means reintroducing a Core `Write`/`Delete` method plus a single trusted/untrusted token distinction, designed and reviewed on its own rather than left half-built here.
- **SQLite full-text index**: replaced by the in-process `internal/search` (literal substring plus RE2 regex, parallel walk).
- **Codebase-to-wiki scanner** (`scan`): generated a wiki from a Go import graph.
- **Standalone JSON API** (`/api/*`): MCP is the machine-readable surface now. The web view's `/graph.json` is internal to the graph view, not a public API.
- **Static export** (`export`): a standalone HTML site.
- **Web theming**: accent and stock configuration, `custom.css`, template overrides.
- **Multi-wiki farm mode**: serving several wikis under `/w/<name>/`.

See [BASELINE-NOTES.md](BASELINE-NOTES.md) for the full record of what was carried forward, what was cut, and the judgment calls behind the dependency and interface choices.

## Development

```bash
go test ./...
CGO_ENABLED=0 go build -o waqwaq .
```

Developed with AI assistance.

## License

MIT. See [LICENSE](LICENSE).

The tree icon is by JK KIM from the [Noun Project](https://thenounproject.com).
