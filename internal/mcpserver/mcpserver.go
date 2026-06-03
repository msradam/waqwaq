// Package mcpserver exposes the wiki over MCP using the Karpathy wiki_* tool
// convention. Identity comes from the caller's bearer token: trusted principals
// commit directly, others have their writes queued as proposals for human
// review. Read tools are always available; write tools require read-write mode.
package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/msradam/waqwaq/internal/auth"
	"github.com/msradam/waqwaq/internal/lint"
	"github.com/msradam/waqwaq/internal/review"
	"github.com/msradam/waqwaq/internal/search"
	"github.com/msradam/waqwaq/internal/store"
)

const baseInstructions = `This is a Waqwaq wiki: git-backed markdown pages that humans browse and agents maintain.

Read with wiki_list, wiki_read, wiki_search, and wiki_graph (the page link graph).
Raw documents to synthesise from live under raw/: list them with wiki_list_raw, read with wiki_read_raw, add with wiki_ingest.
Create or replace pages with wiki_write. Each page needs YAML frontmatter with a title, and links other pages with [[slug]] or [[slug|label]] wikilinks.
wiki_lint dry-runs the checks. A missing title blocks a write; unresolved wikilinks are warnings.
Depending on your access, a write either commits straight to git or is queued as a proposal for a human to approve. Check the status field returned by wiki_write, and list the queue with wiki_list_proposals.`

type Options struct {
	ReadOnly    bool
	ForceReview bool
	Rules       lint.Rules
	Search      search.Searcher
}

type noArgs struct{}

type pageRef struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

func New(st *store.Store, q *review.Queue, reg *auth.Registry, opts Options) *mcp.Server {
	instructions := baseInstructions
	if schema := st.Instructions(); schema != "" {
		instructions += "\n\n--- wiki schema (CLAUDE.md) ---\n\n" + schema
	}

	s := mcp.NewServer(
		&mcp.Implementation{Name: "waqwaq", Title: "Waqwaq wiki", Version: "0.2.0"},
		&mcp.ServerOptions{Instructions: instructions},
	)

	var searcher search.Searcher = st
	if opts.Search != nil {
		searcher = opts.Search
	}

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
		hits, err := searcher.Search(in.Query)
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
		issues := lint.Check(fm, body, known, opts.Rules)
		return nil, lintOut{Issues: issues, OK: !lint.HasErrors(issues)}, nil
	})

	type proposalRef struct {
		ID      string `json:"id"`
		Slug    string `json:"slug"`
		Title   string `json:"title"`
		Author  string `json:"author"`
		Status  string `json:"status"`
		Stale   bool   `json:"stale"`
		Created string `json:"created"`
	}
	type listProposalsOut struct {
		Proposals []proposalRef `json:"proposals"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_list_proposals",
		Description: "List proposals in the review queue: pending writes awaiting human approval, plus recently merged or rejected ones.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, listProposalsOut, error) {
		ps, err := q.List()
		if err != nil {
			return nil, listProposalsOut{}, err
		}
		out := listProposalsOut{}
		for _, p := range ps {
			out.Proposals = append(out.Proposals, proposalRef{
				ID: p.ID, Slug: p.Slug, Title: p.Title, Author: p.Author,
				Status: string(p.Status), Stale: q.Stale(p), Created: p.Created.Format("2006-01-02T15:04:05Z"),
			})
		}
		return nil, out, nil
	})

	if opts.ReadOnly {
		return s
	}

	type writeIn struct {
		Slug    string `json:"slug" jsonschema:"slug of the page to create or replace, e.g. concepts/mcp"`
		Content string `json:"content" jsonschema:"full markdown including YAML frontmatter with a title"`
		Message string `json:"message,omitempty" jsonschema:"commit message describing the change"`
	}
	type writeOut struct {
		Status     string       `json:"status" jsonschema:"committed, proposed, or rejected"`
		Committed  bool         `json:"committed"`
		ProposalID string       `json:"proposal_id,omitempty"`
		Issues     []lint.Issue `json:"issues"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_write",
		Description: "Create or replace a wiki page. Lint runs first; errors block it. A trusted caller commits directly to git; otherwise the write is queued as a proposal for a human to approve. Check the returned status.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in writeIn) (*mcp.CallToolResult, writeOut, error) {
		principal := principalFrom(req, reg)
		known, err := st.KnownSlugs()
		if err != nil {
			return nil, writeOut{}, err
		}
		fm, body := store.SplitFrontmatter(in.Content)
		issues := lint.Check(fm, body, known, opts.Rules)
		if lint.HasErrors(issues) {
			return &mcp.CallToolResult{IsError: true}, writeOut{Status: "rejected", Issues: issues}, nil
		}

		if opts.ForceReview || !principal.Trusted {
			p, err := q.Create(in.Slug, in.Content, principal.Name, issues)
			if err != nil {
				return nil, writeOut{}, err
			}
			return nil, writeOut{Status: "proposed", ProposalID: p.ID, Issues: issues}, nil
		}

		message := in.Message
		if message == "" {
			message = fmt.Sprintf("waqwaq: update %s", in.Slug)
		}
		if err := st.Write(in.Slug, in.Content, fmt.Sprintf("%s <agent@waqwaq.local>", principal.Name), message); err != nil {
			return nil, writeOut{}, err
		}
		return nil, writeOut{Status: "committed", Committed: true, Issues: issues}, nil
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

func principalFrom(req *mcp.CallToolRequest, reg *auth.Registry) auth.Principal {
	var header string
	if req != nil && req.Extra != nil && req.Extra.Header != nil {
		header = req.Extra.Header.Get("Authorization")
	}
	if p, ok := reg.Resolve(header); ok {
		return p
	}
	return auth.Principal{Name: "unknown"}
}
