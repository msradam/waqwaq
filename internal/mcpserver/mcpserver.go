// Package mcpserver exposes the wiki over MCP using the Karpathy wiki_* tool
// convention. Identity comes from the caller's bearer token: trusted principals
// commit directly, others have their writes queued as proposals for human
// review. Read tools are always available; write tools require read-write mode.
package mcpserver

import (
	"context"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/msradam/waqwaq/internal/auth"
	"github.com/msradam/waqwaq/internal/lint"
	"github.com/msradam/waqwaq/internal/review"
	"github.com/msradam/waqwaq/internal/search"
	"github.com/msradam/waqwaq/internal/store"
)

const baseInstructions = `This is a Waqwaq wiki: git-backed markdown pages that humans browse and agents maintain.

Read with wiki_list, wiki_read, wiki_search, and wiki_graph (the page link graph).
Navigate by relationship: wiki_hubs lists the most-connected pages to read first, wiki_neighbors pulls a page's linked neighbourhood in one call, wiki_path returns the chain connecting two pages, and wiki_backlinks lists what links to a page.
Maintain the wiki with wiki_health (orphans, broken links, stale pages), wiki_recent (recent changes), wiki_history, and wiki_tags. Use wiki_health to find what needs fixing.
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

// ServeStdio runs an MCP server over stdin/stdout, the transport agents use to
// launch a local MCP server as a subprocess. It blocks until the client closes
// the connection. Nothing may be written to stdout except the protocol.
func ServeStdio(ctx context.Context, srv *mcp.Server) error {
	return srv.Run(ctx, &mcp.StdioTransport{})
}

func New(st *store.Store, q *review.Queue, reg *auth.Registry, opts Options) *mcp.Server {
	instructions := baseInstructions
	if schema := st.Instructions(); schema != "" {
		instructions += "\n\n--- wiki schema (CLAUDE.md) ---\n\n" + schema
	}

	s := mcp.NewServer(
		&mcp.Implementation{Name: "waqwaq", Title: "Waqwaq wiki", Version: "0.4.0"},
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

	type slugIn struct {
		Slug string `json:"slug" jsonschema:"slug of the page"`
	}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_backlinks",
		Description: "List the pages that link to a given page.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in slugIn) (*mcp.CallToolResult, listOut, error) {
		metas, err := st.Backlinks(in.Slug)
		if err != nil {
			return nil, listOut{}, err
		}
		out := listOut{}
		for _, m := range metas {
			out.Pages = append(out.Pages, pageRef{Slug: m.Slug, Title: m.Title})
		}
		return nil, out, nil
	})

	type neighborsIn struct {
		Slug  string `json:"slug" jsonschema:"slug of the page to start from"`
		Depth int    `json:"depth,omitempty" jsonschema:"how many hops out to include (default 1)"`
	}
	type neighborsOut struct {
		Neighbors []store.Neighbor `json:"neighbors"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_neighbors",
		Description: "List the pages connected to a page within N hops over the link graph, nearest first. Use this to pull a page's surrounding context in one call instead of guessing related pages.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in neighborsIn) (*mcp.CallToolResult, neighborsOut, error) {
		depth := in.Depth
		if depth <= 0 {
			depth = 1
		}
		nb, err := st.Neighbors(in.Slug, depth)
		if err != nil {
			return nil, neighborsOut{}, err
		}
		return nil, neighborsOut{Neighbors: nb}, nil
	})

	type pathIn struct {
		From string `json:"from" jsonschema:"slug of the page to start from"`
		To   string `json:"to" jsonschema:"slug of the page to reach"`
	}
	type pathOut struct {
		Path []pageRef `json:"path"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_path",
		Description: "Find the shortest chain of linked pages connecting two pages, inclusive of both ends, or empty if they are not connected. Use this to answer how one topic relates to another.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in pathIn) (*mcp.CallToolResult, pathOut, error) {
		metas, err := st.Path(in.From, in.To)
		if err != nil {
			return nil, pathOut{}, err
		}
		out := pathOut{}
		for _, m := range metas {
			out.Path = append(out.Path, pageRef{Slug: m.Slug, Title: m.Title})
		}
		return nil, out, nil
	})

	type hubsIn struct {
		Limit int `json:"limit,omitempty" jsonschema:"maximum number of pages to return (default 10)"`
	}
	type hubsOut struct {
		Hubs []store.Hub `json:"hubs"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_hubs",
		Description: "List the most-connected pages by number of links, the natural entry points into an unfamiliar wiki. Read these first to orient.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in hubsIn) (*mcp.CallToolResult, hubsOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 10
		}
		hubs, err := st.Hubs(limit)
		if err != nil {
			return nil, hubsOut{}, err
		}
		return nil, hubsOut{Hubs: hubs}, nil
	})

	type healthOut struct {
		Orphans []pageRef          `json:"orphans"`
		Broken  []store.BrokenLink `json:"broken"`
		Stale   []store.StalePage  `json:"stale"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_health",
		Description: "Find pages that need attention: orphans (no incoming links), broken wikilinks, and stale pages (untouched for 90+ days). Use this to decide what to fix.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, healthOut, error) {
		h, err := st.Health()
		if err != nil {
			return nil, healthOut{}, err
		}
		out := healthOut{Broken: h.Broken, Stale: h.Stale}
		for _, o := range h.Orphans {
			out.Orphans = append(out.Orphans, pageRef{Slug: o.Slug, Title: o.Title})
		}
		return nil, out, nil
	})

	type recentIn struct {
		Limit int `json:"limit,omitempty" jsonschema:"maximum number of changes to return (default 20)"`
	}
	type recentOut struct {
		Changes []store.Change `json:"changes"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_recent",
		Description: "List recently changed pages, newest first.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in recentIn) (*mcp.CallToolResult, recentOut, error) {
		limit := in.Limit
		if limit <= 0 {
			limit = 20
		}
		changes, err := st.Recent(limit)
		if err != nil {
			return nil, recentOut{}, err
		}
		return nil, recentOut{Changes: changes}, nil
	})

	type historyOut struct {
		Revisions []store.Revision `json:"revisions"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_history",
		Description: "List the git revisions of a page, newest first.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in slugIn) (*mcp.CallToolResult, historyOut, error) {
		revs, err := st.History(in.Slug)
		if err != nil {
			return nil, historyOut{}, err
		}
		return nil, historyOut{Revisions: revs}, nil
	})

	type tagGroup struct {
		Tag   string    `json:"tag"`
		Pages []pageRef `json:"pages"`
	}
	type tagsOut struct {
		Tags []tagGroup `json:"tags"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_tags",
		Description: "List every tag and the pages that carry it.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, tagsOut, error) {
		tags, err := st.Tags()
		if err != nil {
			return nil, tagsOut{}, err
		}
		names := make([]string, 0, len(tags))
		for name := range tags {
			names = append(names, name)
		}
		sort.Strings(names)
		out := tagsOut{}
		for _, name := range names {
			g := tagGroup{Tag: name}
			for _, m := range tags[name] {
				g.Pages = append(g.Pages, pageRef{Slug: m.Slug, Title: m.Title})
			}
			out.Tags = append(out.Tags, g)
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
