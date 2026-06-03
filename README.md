# Waqwaq

A git-backed markdown wiki that humans browse and AI agents read and write, served from one binary over one port.

## What it is

Waqwaq serves a directory of markdown files two ways at once. People get a web UI for reading and searching pages. AI agents get a Model Context Protocol (MCP) endpoint on the same port, with tools to read the wiki and to create or update pages. Every page is a markdown file on disk, versioned with git, so changes have history, blame, and rollback.

Writes from agents pass through a lint step before they land. A page without a frontmatter title is rejected, and links that point at missing pages are flagged. Each accepted write is committed to git with the author recorded, so you can see which agent or person last touched a page.

The layout follows Andrej Karpathy's LLM wiki convention: pages under `wiki/`, raw source documents under `raw/`, and a `CLAUDE.md` schema at the root. If you point Waqwaq at a folder that has no `wiki/` subdirectory, it serves the folder itself, so an existing notes folder or Obsidian vault works without restructuring.

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

Pages are markdown with optional YAML frontmatter. A `title` field sets the page title; without one, the first heading or the file name is used. Link between pages with `[[slug]]` or `[[slug|label]]` wikilinks. Fenced `mermaid` blocks render as diagrams, and fenced code blocks are syntax highlighted.

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

Pending proposals appear at `/proposals` in the web UI, each with a line diff and Approve or Reject buttons. Approving writes the page and commits it, recording the proposer as the git author and the approver in the commit message. The `.waqwaq/` directory holds tokens and pending proposals and is kept out of the wiki's git history.

The web UI is unauthenticated and assumes a local, trusted operator. Do not expose it to an untrusted network.

## Development

```bash
go test ./...
go build -o waqwaq .
```

Waqwaq was developed with AI assistance.

## License

MIT. See [LICENSE](LICENSE).
