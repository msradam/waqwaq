---
title: MCP integration
---

# MCP integration

Waqwaq serves a Model Context Protocol endpoint at `/mcp` (streamable HTTP) from the same
process as the web UI.

## Tools

- Read the wiki with `wiki_list`, `wiki_read`, `wiki_search`, and `wiki_graph`.
- Work with raw documents under `raw/` using `wiki_list_raw`, `wiki_read_raw`, and `wiki_ingest`.
- Dry-run the write checks with `wiki_lint`.
- Create or replace a page with `wiki_write`. Lint runs first; errors block the write.

## Example handler

```go
mcp.AddTool(s, &mcp.Tool{Name: "wiki_read"}, func(
	ctx context.Context, req *mcp.CallToolRequest, in readIn,
) (*mcp.CallToolResult, readOut, error) {
	page, err := st.Read(in.Slug)
	if err != nil {
		return nil, readOut{}, err
	}
	return nil, readOut{Slug: page.Slug, Title: page.Title, Content: page.Raw}, nil
})
```

Back to [[index]].
