# MCP reference

waqwaq exposes a read-only OKF wiki over the [Model Context Protocol](https://modelcontextprotocol.io). This is the reference for the resources, tools, and their shapes. For install and overview, see the [README](../README.md).

## Connecting

Streamable HTTP (from `waqwaq serve`):

```json
{ "mcpServers": { "wiki": { "type": "http", "url": "http://127.0.0.1:8000/mcp" } } }
```

Stdio subprocess (no running server):

```json
{ "mcpServers": { "wiki": { "command": "waqwaq", "args": ["mcp", "/path/to/bundle"] } } }
```

The server refuses to start on a bundle that is not OKF compliant (every concept page must carry a `type`). Pass `--lenient` to serve non-OKF markdown.

## Server instructions

On `initialize` the server sends instructions naming the wiki (its `title`), how to read pages, and the orient → filter → navigate flow. If the bundle has a `CLAUDE.md`, its contents are appended as the wiki schema. Call `wiki_info` at any time to confirm which wiki you are connected to.

## Slugs

A slug is a page's path under the wiki root without the `.md` extension: `wiki/tables/customers.md` → `tables/customers`. `index.md` and `log.md` are reserved (navigation, not concepts): they are readable but excluded from listings.

## Resources

Resources are the primary read interface.

| | |
|---|---|
| Template | `wiki://page/{slug}` (e.g. `wiki://page/tables/customers`) |
| `resources/read` | returns the page's markdown body (`text/markdown`) |
| `resources/list` | enumerates every concept page (frontmatter metadata only) |

The resource returns the **body only**. For navigation prefer the `wiki_read` tool, which also returns parsed frontmatter and resolved inbound/outbound links.

## Tools

All tools are annotated read-only (`readOnlyHint`, `idempotentHint`). There are no mutation tools. Results are returned as `structuredContent`.

### Discovery

| Tool | Args | Returns |
|---|---|---|
| `wiki_info` | — | `{title, description, pages, versioned}` |
| `wiki_list` | `prefix?`, `type?`, `tag?`, `limit?` (500), `offset?` | `{pages: PageMeta[], total, truncated}` |
| `wiki_search` | `query`, `regex?`, `type?`, `tag?`, `limit?` (100) | `{hits: Match[]}` |
| `wiki_tags` | `tag?` | `{tags: TagCount[]}` |
| `wiki_recent` | `limit?` (20) | `{pages: PageMeta[]}` |
| `wiki_hubs` | `limit?` (10) | `{hubs: GraphNode[]}` |

### Reading and navigating

| Tool | Args | Returns |
|---|---|---|
| `wiki_read` | `slug` | `Page` (body + inlined `outbound`/`inbound` edges) |
| `wiki_backlinks` | `slug` | `{backlinks: string[]}` |
| `wiki_neighbors` | `slug`, `depth?` (1, max 3) | `{neighbors: GraphNode[]}` |
| `wiki_graph` | — | `{nodes: GraphNode[], edges: GraphEdge[]}` |
| `wiki_history` | `slug`, `limit?` (20) | `{versioned, commits: Commit[]}` |

### Notes on behavior

- **`wiki_search`** is keyword-AND: every whitespace-separated term must appear in the page (case-insensitive). Set `regex: true` to treat `query` as one RE2 pattern instead. Ordering is deterministic by slug.
- **`type` and `tag` filters** match case-insensitively.
- **`wiki_list` pagination**: `total` is the full match count; `truncated` is true when more match than were returned. Page with `offset`.
- **`wiki_hubs`** ranks concept pages only (reserved files excluded); **`wiki_graph`** and **`wiki_neighbors`** include reserved files as nodes, since they carry structure.
- **`wiki_history`**: if `versioned` is false the wiki is not a git repo, so empty `commits` means history is unavailable, not that the page is unchanged.

## Object shapes

```
PageMeta   { slug, title, type?, description?, resource?, tags?[], timestamp? }
Page       { slug, title, frontmatter{}, body, outbound: EdgeRef[], inbound: EdgeRef[] }
EdgeRef    { slug, title?, type? }
Match      { slug, title, type?, tags?[], context }
GraphNode  { slug, title, type?, degree }
GraphEdge  { from, to }
TagCount   { tag, count }
Commit     { hash, author, date, message }
```

`resource` is an external deep-link to the underlying asset (e.g. a BigQuery table URL); it is not a wiki page and not a graph edge.

## Orienting in an unfamiliar wiki

A typical agent flow:

1. `wiki_info` — confirm the wiki, see the page count.
2. `wiki_read` `index`, or `wiki_hubs` — the entry point and the most-connected pages.
3. `wiki_list` with `type` / `prefix` / `tag`, or `wiki_search` — narrow to what you need.
4. `wiki_read` a page — its `outbound`/`inbound` edges carry each neighbour's slug, title, and type, so you navigate without a read per neighbour.
5. `wiki_neighbors` / `wiki_backlinks` — expand the neighbourhood or find what links in.

## Errors

- Reading a missing slug returns a tool error (not a panic).
- In OKF mode, reading a concept page that lacks `type` errors; `index.md`/`log.md` are exempt.
- A bad regex in `wiki_search` returns a compile error.

## Determinism for testing

The MCP surface is deterministic: same bundle, same call, same result. The repository's [`perf/`](../perf) directory holds [ocarina](https://github.com/msradam/ocarina) rondos (`smoke.yaml`, `okf-bundle.yaml`) that replay tool sequences and assert on results, doubling as CI checks.
