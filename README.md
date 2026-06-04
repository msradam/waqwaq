<p align="center">
  <img src="noun-tree-8262937.png" alt="" width="84">
</p>

# Waqwaq

A git-backed markdown wiki that humans browse and AI agents read and write, from one binary over one port.

## What it is

Waqwaq serves a folder of markdown two ways at once: a web UI for people to read, search, and edit, and a Model Context Protocol (MCP) endpoint on the same port for agents to read and write. Every page is a markdown file versioned with git, so changes have history, blame, and rollback. Agent writes pass a lint step first (a frontmatter `title` is required, broken links are flagged) and commit with the author recorded.

The native layout is Andrej Karpathy's LLM wiki convention: pages under `wiki/`, raw documents under `raw/`, a `CLAUDE.md` schema at the root. Point it at a folder with no `wiki/` subdirectory and it serves the folder itself, so an existing notes folder or Obsidian vault works as is.

## Quickstart

```bash
go install github.com/msradam/waqwaq@latest   # or: go build -o waqwaq .
waqwaq init mywiki                             # or point serve at an existing folder
waqwaq serve mywiki
```

The web UI is at `http://127.0.0.1:8000/`, the MCP endpoint at `/mcp`. Connect an agent with a `.mcp.json`:

```json
{ "mcpServers": { "waqwaq": { "type": "http", "url": "http://127.0.0.1:8000/mcp" } } }
```

Or, with no running server, as a stdio subprocess:

```json
{ "mcpServers": { "waqwaq": { "command": "waqwaq", "args": ["mcp", "/path/to/wiki"] } } }
```

Ask the agent to read the wiki and add a page: it writes markdown to disk, lint-checked and committed to git, and the page appears in the browser. Run with `--review` and agent writes become proposals you approve from `/proposals` with a diff; the merge records who proposed and who approved.

## Surfaces

One folder, reached however you work. The read surfaces share one core, so their answers are identical.

- **Web UI** (`waqwaq serve`): browse, search, edit, image upload, plus the `/health` "Canopy" (orphans, broken and stale links) and the `/oracle` force-directed link graph.
- **MCP**: streamable HTTP at `/mcp`, or stdio via `waqwaq mcp <dir>`.
- **CLI**: `waqwaq toc | grep | cat --render`, scriptable, with `--tag` / `--links-to` to scope search by the graph and `--json` for pipelines. Add `--remote URL` (or `WAQWAQ_REMOTE`) to query a running server.
- **TUI** (`waqwaq tui <dir>`): a terminal reader with filter, search, rendered pages, and `r` to walk the link graph.
- **JSON API**: `/api/pages`, `/api/search`, `/api/page/<slug>`, `/api/graph`, and the graph queries, for non-MCP clients.
- **Static export** (`waqwaq export <dir> <out>`): a standalone HTML site for any static host, including GitHub Pages. A static host cannot run the MCP server; to query a hosted wiki over MCP, run `waqwaq mcp` against a clone, or `waqwaq serve` on a small host.
- **Codebase to wiki** (`waqwaq scan <go-repo> <out>`): one page per Go package, linked by the real import graph, deterministic, no model.

Diagnostics: `waqwaq doctor [dir]` checks setup and the MCP/access posture; `waqwaq check [dir]` lints pages and links for CI and exits non-zero on errors.

## MCP tools

- Read: `wiki_list`, `wiki_read`, `wiki_search`, `wiki_graph`.
- Navigate by relationship: `wiki_hubs`, `wiki_neighbors`, `wiki_path`, `wiki_backlinks`.
- Maintain: `wiki_health`, `wiki_recent`, `wiki_history`, `wiki_tags`.
- Raw documents: `wiki_list_raw`, `wiki_read_raw`, `wiki_ingest`.
- Review and write: `wiki_list_proposals`, `wiki_lint`, `wiki_write` (returns `committed`, `proposed`, or `rejected`).

Read tools are always available; the write tools are exposed only when the server is not read-only.

## Existing knowledge bases

The first-class format is the LLM wiki layout (clean markdown, YAML frontmatter, `[[wikilinks]]`), what agents write back to. Waqwaq also reads existing knowledge bases as a compatibility layer, with tolerant link resolution: bare `[[wikilinks]]` resolve by basename anywhere in the tree, case and space and hyphen are folded, and a piped `[[a|b]]` resolves from either side. Frontmatter is read as YAML, TOML, or JSON. Image embeds, heading-anchor links, and plain `[text](page)` markdown links work; wikilink syntax inside code blocks is ignored. So Obsidian vaults, Quartz and Foam gardens, GitHub project wikis, Dendron vaults, and Hugo or Zola sites serve and navigate.

It does not write back in another tool's conventions (agents write clean LLM wiki markdown), nor interpret format-specific models like Logseq block references or Dendron's filename hierarchy; those render as plain text. If a knowledge base depends on them, the native tool serves it better.

## Configuration

`waqwaq serve [dir]` flags: `--addr` (default `127.0.0.1:8000`), `--read-only`, `--review`, `--tokens FILE`. Each has a `WAQWAQ_*` environment equivalent. Pages come from `<dir>/wiki` if it exists, otherwise `<dir>`; raw documents from `<dir>/raw`; `<dir>/CLAUDE.md` is sent to MCP clients as the schema.

Pages are markdown with optional frontmatter. `mermaid` blocks render as diagrams, code is syntax highlighted, and `> [!NOTE]` blockquotes render as callouts. Headings get anchors and a table of contents, the footer shows who last touched a page (and the approver for reviewed writes) with a link to git history, and each page lists its backlinks and `tags` (browsable at `/tags`).

Optional settings live in `<dir>/.waqwaq/config.json`, all fields optional:

```json
{
  "title": "My Wiki", "accent": "#7b2ff7", "theme": "auto",
  "webhook": "https://hooks.slack.com/services/XXX",
  "web": { "proxy_header": "X-Forwarded-User", "default_role": "viewer", "admins": ["adam"], "editors": ["dev"] },
  "lint": { "require_frontmatter": ["owner"], "banned_terms": [ { "term": "TODO", "severity": "warning" } ] }
}
```

`accent` and `theme` (`auto`/`light`/`dark`) style the UI; `webhook` receives a Slack-compatible JSON POST when a write is queued; `lint.require_frontmatter` and `lint.banned_terms` block non-conforming writes. A `<dir>/.waqwaq/custom.css` overrides the built-in styles, and markdown files in `<dir>/.waqwaq/templates/` become new-page starting points.

### Access control

By default writes commit straight to git and the web UI trusts loopback access. To gate the MCP endpoint, create `<dir>/.waqwaq/tokens.json`:

```json
{ "tokens": [
  { "token": "ci-secret",   "name": "ci-bot", "trusted": false },
  { "token": "adam-secret", "name": "adam",   "trusted": true }
] }
```

The MCP endpoint then requires `Authorization: Bearer <token>` (401 otherwise). A `trusted` token commits directly; any other token's writes become proposals. The token's `name` becomes the git author. `--review` queues every write regardless.

For the web UI, set `web.proxy_header` to delegate auth to a reverse proxy (oauth2-proxy, Authelia), or set `web.users` (each `{ "name", "password", "role" }`, the password a bcrypt hash from `waqwaq passwd`) for a built-in login. Roles: `viewer` reads, `editor` also edits and uploads, `admin` also approves proposals. Run `waqwaq doctor` to check the posture before serving. Put a reverse proxy in front for TLS. Pass multiple directories to `serve` to host several wikis under `/w/<name>/`, each scoped to its own git repo, tokens, and config.

## Platforms

Pure Go, no cgo, so it cross-compiles to any `GOOS`/`GOARCH`. Full-text search uses the pure-Go `modernc.org/sqlite` driver; where it does not build (z/OS on s390x), build with `-tags nofts` to drop the FTS index and fall back to substring search (selected automatically when `GOOS=zos`). z/OS itself needs the IBM Open Enterprise SDK for Go.

## Development

```bash
go test ./...
go build -o waqwaq .
```

Developed with AI assistance.

## License

MIT. See [LICENSE](LICENSE).

The tree icon is by JK KIM from the [Noun Project](https://thenounproject.com).
