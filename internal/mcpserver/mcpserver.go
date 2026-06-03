// Package mcpserver exposes the wiki over MCP using the Karpathy wiki_* tool
// convention: read tools always, write tools only when not in read-only mode.
// Writes pass through lint before they land.
package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/msradam/waqwaq/internal/lint"
	"github.com/msradam/waqwaq/internal/store"
)

const baseInstructions = `This is a Waqwaq wiki: git-backed markdown pages that humans browse and agents maintain.

Read with wiki_list, wiki_read, wiki_search, and wiki_graph (the page link graph).
Raw documents to synthesise from live under raw/: list them with wiki_list_raw, read with wiki_read_raw, add with wiki_ingest.
Create or replace pages with wiki_write. Each page needs YAML frontmatter with a title, and links other pages with [[slug]] or [[slug|label]] wikilinks.
wiki_lint dry-runs the checks. A missing title blocks a write; unresolved wikilinks are warnings. Successful writes are committed to git.`

type noArgs struct{}

type pageRef struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

func New(st *store.Store, readOnly bool) *mcp.Server {
	instructions := baseInstructions
	if schema := strings.TrimSpace(st.Instructions()); schema != "" {
		instructions += "\n\n--- wiki schema (CLAUDE.md) ---\n\n" + schema
	}

	s := mcp.NewServer(
		&mcp.Implementation{Name: "waqwaq", Title: "Waqwaq wiki", Version: "0.1.0"},
		&mcp.ServerOptions{Instructions: instructions},
	)

	type listOut struct {
		Pages []pageRef `json:"pages"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_list",
		Description: "List every wiki page with its slug and title.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, listOut, error) {
		metas, err := st.List()
		if err != nil {
			return nil, listOut{}, err
		}
		out := listOut{}
		for _, m := range metas {
			out.Pages = append(out.Pages, pageRef{Slug: m.Slug, Title: m.Title})
		}
		return nil, out, nil
	})

	type readIn struct {
		Slug string `json:"slug" jsonschema:"slug of the page, e.g. concepts/mcp"`
	}
	type readOut struct {
		Slug    string `json:"slug"`
		Title   string `json:"title"`
		Content string `json:"content" jsonschema:"full markdown including YAML frontmatter"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_read",
		Description: "Read a single wiki page by slug, returning its full markdown.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in readIn) (*mcp.CallToolResult, readOut, error) {
		page, err := st.Read(in.Slug)
		if err != nil {
			return nil, readOut{}, err
		}
		return nil, readOut{Slug: page.Slug, Title: page.Title, Content: page.Raw}, nil
	})

	type searchIn struct {
		Query string `json:"query" jsonschema:"text to search for across page titles and bodies"`
	}
	type searchOut struct {
		Hits []store.SearchHit `json:"hits"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_search",
		Description: "Full-text search across all wiki pages.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
		hits, err := st.Search(in.Query)
		if err != nil {
			return nil, searchOut{}, err
		}
		return nil, searchOut{Hits: hits}, nil
	})

	type graphOut struct {
		Pages []pageRef         `json:"pages"`
		Edges []store.GraphEdge `json:"edges"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_graph",
		Description: "Return the wiki as a graph: pages as nodes and resolved [[wikilink]] references as directed edges.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, graphOut, error) {
		metas, edges, err := st.Graph()
		if err != nil {
			return nil, graphOut{}, err
		}
		out := graphOut{Edges: edges}
		for _, m := range metas {
			out.Pages = append(out.Pages, pageRef{Slug: m.Slug, Title: m.Title})
		}
		return nil, out, nil
	})

	type rawListOut struct {
		Documents []string `json:"documents"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_list_raw",
		Description: "List raw source documents in raw/, available to synthesise into pages.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, rawListOut, error) {
		names, err := st.ListRaw()
		if err != nil {
			return nil, rawListOut{}, err
		}
		return nil, rawListOut{Documents: names}, nil
	})

	type rawReadIn struct {
		Name string `json:"name" jsonschema:"file name within the raw/ directory"`
	}
	type rawReadOut struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_read_raw",
		Description: "Read a raw source document by name.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in rawReadIn) (*mcp.CallToolResult, rawReadOut, error) {
		content, err := st.ReadRaw(in.Name)
		if err != nil {
			return nil, rawReadOut{}, err
		}
		return nil, rawReadOut{Name: in.Name, Content: content}, nil
	})

	type lintIn struct {
		Content string `json:"content" jsonschema:"markdown (with frontmatter) to validate without writing"`
	}
	type lintOut struct {
		Issues []lint.Issue `json:"issues"`
		OK     bool         `json:"ok"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_lint",
		Description: "Dry-run the write checks against some markdown without saving it.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in lintIn) (*mcp.CallToolResult, lintOut, error) {
		known, err := st.KnownSlugs()
		if err != nil {
			return nil, lintOut{}, err
		}
		fm, body := store.SplitFrontmatter(in.Content)
		issues := lint.Check(fm, body, known)
		return nil, lintOut{Issues: issues, OK: !lint.HasErrors(issues)}, nil
	})

	if readOnly {
		return s
	}

	type writeIn struct {
		Slug    string `json:"slug" jsonschema:"slug of the page to create or replace, e.g. concepts/mcp"`
		Content string `json:"content" jsonschema:"full markdown including YAML frontmatter with a title"`
		Author  string `json:"author,omitempty" jsonschema:"who is making the change, e.g. claude-opus-4-8"`
		Message string `json:"message,omitempty" jsonschema:"commit message describing the change"`
	}
	type writeOut struct {
		Committed bool         `json:"committed"`
		Issues    []lint.Issue `json:"issues"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_write",
		Description: "Create or replace a wiki page. Lint runs first; errors block the write. On success the change is committed to git.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in writeIn) (*mcp.CallToolResult, writeOut, error) {
		known, err := st.KnownSlugs()
		if err != nil {
			return nil, writeOut{}, err
		}
		fm, body := store.SplitFrontmatter(in.Content)
		issues := lint.Check(fm, body, known)
		if lint.HasErrors(issues) {
			return &mcp.CallToolResult{IsError: true}, writeOut{Committed: false, Issues: issues}, nil
		}
		author := in.Author
		if author == "" {
			author = "agent"
		}
		message := in.Message
		if message == "" {
			message = fmt.Sprintf("waqwaq: update %s", in.Slug)
		}
		if err := st.Write(in.Slug, in.Content, fmt.Sprintf("%s <agent@waqwaq.local>", author), message); err != nil {
			return nil, writeOut{}, err
		}
		return nil, writeOut{Committed: true, Issues: issues}, nil
	})

	type ingestIn struct {
		Name    string `json:"name" jsonschema:"file name to store under raw/, e.g. interview-transcript.md"`
		Content string `json:"content" jsonschema:"the raw document text"`
	}
	type ingestOut struct {
		Stored string `json:"stored"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_ingest",
		Description: "Store a raw source document under raw/ for later synthesis into pages. Does not create a page.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in ingestIn) (*mcp.CallToolResult, ingestOut, error) {
		if err := st.AddRaw(in.Name, []byte(in.Content)); err != nil {
			return nil, ingestOut{}, err
		}
		return nil, ingestOut{Stored: "raw/" + in.Name}, nil
	})

	return s
}
