# Waqwaq

A git-backed markdown wiki that humans browse and AI agents read and write, served from one binary over one port.

## What it is

Waqwaq serves a directory of markdown files two ways at once. People get a web UI for reading, searching, and editing pages, including image upload. AI agents get a Model Context Protocol (MCP) endpoint on the same port, with tools to read the wiki and to create or update pages. Every page is a markdown file on disk, versioned with git, so changes have history, blame, and rollback.

Writes from agents pass through a lint step before they land. A page without a frontmatter title is rejected, and links that point at missing pages are flagged. Each accepted write is committed to git with the author recorded, so you can see which agent or person last touched a page.

The layout follows Andrej Karpathy's LLM wiki convention: pages under `wiki/`, raw source documents under `raw/`, and a `CLAUDE.md` schema at the root. If you point Waqwaq at a folder that has no `wiki/` subdirectory, it serves the folder itself, so an existing notes folder or Obsidian vault works without restructuring.

## Quickstart

From zero to an AI-maintained wiki in about two minutes.

### 1. Install

```bash
go install github.com/msradam/waqwaq@latest
```

Or download a binary from the [releases page](https://github.com/msradam/waqwaq/releases).

### 2. Create a wiki and serve it

```bash
waqwaq init mywiki
waqwaq serve mywiki
```

Open `http://127.0.0.1:8000`. You have a browsable wiki with two sample pages, full-text search, and a Mermaid diagram on the home page.

Already have notes? Skip `init` and point Waqwaq at any markdown folder or Obsidian vault:

```bash
waqwaq serve ~/notes
```

### 3. Connect an AI agent

Create `.mcp.json` next to where you run Claude Code:

```json
{
  "mcpServers": {
    "waqwaq": {
      "type": "http",
      "url": "http://127.0.0.1:8000/mcp"
    }
  }
}
```

Launch `claude`, run `/mcp`, and approve `waqwaq`. The agent now has the `wiki_*` tools.

### 4. Have the agent read and write

Ask Claude:

> Read the wiki, then add a page documenting our deploy process and link it from the index.

The page is written to disk, committed to git with the agent recorded as the author, and appears in the browser immediately. Lint runs first, so a page without a title is rejected before it lands.

### 5. Add review (optional)

Run with `--review`, and AI writes become proposals instead of commits:

```bash
waqwaq serve mywiki --review
```

Now the same request queues a proposal. Open `/proposals`, read the diff, and click Approve. The merge commit records who proposed the change and who approved it. That is the whole loop: humans browse, agents maintain, every change is reviewable markdown in git.

## Install

With Go 1.26 or newer:

```bash
go install github.com/msradam/waqwaq@latest
```

Or clone and build:

```bash
git clone https://github.com/msradam/waqwaq
cd waqwaq
go build -o waqwaq .
```

## Usage

Scaffold a new wiki:

```bash
waqwaq init mywiki
```

This creates `mywiki/wiki/`, `mywiki/raw/`, and `mywiki/CLAUDE.md`, with two sample pages.

Serve it:

```bash
waqwaq serve mywiki
```

The web UI is at `http://127.0.0.1:8000/` and the MCP endpoint is at `http://127.0.0.1:8000/mcp`.

Point Waqwaq at any markdown folder or Obsidian vault:

```bash
waqwaq serve ~/notes
```

### Connect an agent

The MCP endpoint speaks streamable HTTP. To use it from Claude Code, add a project `.mcp.json`:

```json
{
  "mcpServers": {
    "waqwaq": {
      "type": "http",
      "url": "http://127.0.0.1:8000/mcp"
    }
  }
}
```

The server exposes these tools:

- `wiki_list`, `wiki_read`, `wiki_search`, `wiki_graph` read the wiki.
- `wiki_health`, `wiki_recent`, `wiki_backlinks`, `wiki_history`, `wiki_tags` find what needs attention and navigate the wiki. Ask the agent to "fix the broken links" or "update the stalest runbooks" and it can use `wiki_health` to find them.
- `wiki_list_raw`, `wiki_read_raw`, `wiki_ingest` work with raw documents under `raw/`.
- `wiki_list_proposals` lists the review queue.
- `wiki_lint` dry-runs the write checks.
- `wiki_write` creates or replaces a page. Depending on the caller's access it either commits or is queued for review (see [Review and access control](#review-and-access-control)). The returned `status` is `committed`, `proposed`, or `rejected`.

Read tools are always available. The write tools, `wiki_write` and `wiki_ingest`, are exposed only when the server is not in read-only mode.

### Example

The repository includes a small example wiki about the Waq-Waq myth:

```bash
waqwaq serve examples/waqwaq-myth
```

### Static export

Render the wiki to a standalone static site (no server, no AI), for publishing or backup:

```bash
waqwaq export ./wiki ./site
```

Serve the output directory with any static file server.

### Multiple wikis

Pass more than one directory and each is served as a separate wiki under a path prefix:

```bash
waqwaq serve ~/kb/platform ~/kb/security
```

`/` lists the wikis, `/w/platform/` and `/w/security/` serve them, and each has its own MCP endpoint at `/w/<name>/mcp`. Every wiki keeps its own git repo, search index, review queue, config, and `.waqwaq/tokens.json`, so access is scoped per wiki: a token issued for one wiki is not accepted by another, and pages cannot cross between roots. A single directory still serves at the root unchanged. Put a reverse proxy in front for TLS.

## Configuration

`waqwaq serve [dir]` accepts:

- `--addr` sets the listen address. The default is `127.0.0.1:8000`.
- `--read-only` disables writes. The web UI and read tools stay available, and the write tools are not exposed. `WAQWAQ_READ_ONLY=1` does the same.
- `--review` queues every write for human review instead of committing. `WAQWAQ_REVIEW=1` does the same.
- `--tokens FILE` points at a tokens file. The default is `<dir>/.waqwaq/tokens.json`. `WAQWAQ_TOKENS` does the same.

Layout:

- Pages are served from `<dir>/wiki` if that directory exists, otherwise from `<dir>` itself.
- Raw documents live in `<dir>/raw`.
- `<dir>/CLAUDE.md`, if present, is sent to MCP clients as the server instructions, so an agent reads the wiki's schema before it writes.

Pages are markdown with optional YAML frontmatter. A `title` field sets the page title; without one, the first heading or the file name is used. Link between pages with `[[slug]]` or `[[slug|label]]` wikilinks. Fenced `mermaid` blocks render as diagrams, fenced code blocks are syntax highlighted, and `> [!NOTE]` or `> [!WARNING]` blockquotes render as callouts (the GitHub and Obsidian convention). Headings get anchor links and a right-rail table of contents, the sidebar groups pages into a collapsible tree, and each page footer shows who last touched it (including the proposer and approver for reviewed writes) with a link to its git history. Each page also lists the pages that reference it and its frontmatter `tags`, which are browsable at `/tags`. The `/health` view (the "Canopy") surfaces orphan pages, broken links, stale pages, and recent changes, the same data the `wiki_health` tool gives an agent.

### Review and access control

By default, writes commit straight to git. To require human review, either pass `--review`, which queues every write, or configure tokens so that only trusted callers commit directly.

Create `<dir>/.waqwaq/tokens.json`:

```json
{
  "tokens": [
    { "token": "a-long-random-secret", "name": "ci-bot", "trusted": false },
    { "token": "another-secret", "name": "adam", "trusted": true }
  ]
}
```

When a tokens file is present, the MCP endpoint requires `Authorization: Bearer <token>`; a request without a valid token gets a 401. A write from a `trusted` token commits directly. A write from any other token becomes a proposal. The principal's `name` is recorded as the author, so attribution comes from the token rather than from the request body. Agents send the token through `.mcp.json`:

```json
{
  "mcpServers": {
    "waqwaq": {
      "type": "http",
      "url": "http://127.0.0.1:8000/mcp",
      "headers": { "Authorization": "Bearer a-long-random-secret" }
    }
  }
}
```

Pending proposals appear at `/proposals` in the web UI, each with a line diff and Approve or Reject buttons. Approving writes the page and commits it, recording the proposer as the git author and the approver in the commit message. The `tokens.json` and `proposals/` entries under `.waqwaq/` are kept out of the wiki's git history; the settings below are versioned with the wiki.

By default the web UI is open and trusts local access: on a loopback bind anyone local can read and edit, and mutating actions (edit, upload, approve) are refused from non-loopback connections without a trusted token.

To require authentication, set `web.proxy_header` to the header your reverse proxy injects after it authenticates the user, for example `X-Forwarded-User` from oauth2-proxy or Authelia. Login and SSO/OIDC are handled by the proxy, so they are not built into the binary. With a proxy header set, every UI request needs an authenticated user, and roles control what they can do: `viewer` reads, `editor` also edits and uploads, `admin` also approves proposals. List people under `web.admins` and `web.editors`; everyone else gets `web.default_role`. The MCP endpoint keeps its own bearer-token auth, independent of the web header.

### Appearance and tuning

Optional settings live in `<dir>/.waqwaq/config.json`. Every field is optional, and a missing file uses defaults.

```json
{
  "title": "My Wiki",
  "accent": "#7b2ff7",
  "theme": "auto",
  "addr": "127.0.0.1:8000",
  "review": false,
  "webhook": "https://hooks.slack.com/services/XXX",
  "web": { "proxy_header": "X-Forwarded-User", "default_role": "viewer", "admins": ["adam"], "editors": ["dev"] },
  "lint": {
    "require_frontmatter": ["owner"],
    "banned_terms": [
      { "term": "TODO", "message": "resolve TODOs before publishing", "severity": "warning" }
    ]
  }
}
```

- `title` sets the brand and the page-title suffix.
- `accent` is a CSS color for links and highlights.
- `theme` is `auto` (follow the browser), `light`, or `dark`.
- `addr` and `review` set defaults for the matching flags; an explicit flag still wins.
- `webhook` is a URL that receives a JSON POST when a write is queued for review. The payload includes a Slack-compatible `text` field, so a Slack incoming webhook works directly.
- `lint.require_frontmatter` lists frontmatter fields that must be present, in addition to the always-required `title`. A missing one blocks the write.
- `lint.banned_terms` flags page bodies containing a term. `severity` is `warning` (default) or `error`, and an `error` blocks the write.

For a full restyle, add `<dir>/.waqwaq/custom.css`. It loads after the built-in stylesheet, so you can override any rule or CSS variable, including the theme colors. Add it before starting the server.

Drop markdown files in `<dir>/.waqwaq/templates/` to offer them as starting points when creating a page (an ADR or postmortem skeleton, for example). They appear on the new-page editor.

Search uses SQLite FTS5 with prefix matching, rebuilt automatically when pages change. The driver is pure Go, so the binary stays a single static file.

## Development

```bash
go test ./...
go build -o waqwaq .
```

Waqwaq was developed with AI assistance.

## License

MIT. See [LICENSE](LICENSE).
