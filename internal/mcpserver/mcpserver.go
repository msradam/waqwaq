// Package mcpserver exposes a read-only OKF wiki over MCP. Resources are the
// primary read interface: resource template wiki://page/{slug} reads any page
// straight from disk, and resources/list enumerates pages (frontmatter-only) by
// re-walking the tree per call. Tools cover the genuine queries agents make —
// list, search, backlinks, neighbors, tags, hubs, graph, history — plus a
// wiki_read tool so a client without the resources primitive can still navigate.
//
// There are no mutation tools. This baseline is read-only across every surface.
package mcpserver

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/msradam/waqwaq/core"
	"github.com/msradam/waqwaq/internal/version"
)

const uriPrefix = "wiki://page/"

const instructionsSuffix = `
This is a read-only OKF (Open Knowledge Format) wiki. Pages are markdown with YAML frontmatter (type, description, resource, tags, timestamp).

Read a page with the resource wiki://page/{slug} or the wiki_read tool; both return the body plus inlined outbound and inbound links (each with slug, title, and OKF type) so you can navigate without a read per neighbor.

Confirm which wiki you are connected to with wiki_info.
Orient in an unfamiliar wiki: start at wiki_read("index"), or wiki_hubs to see the most-connected pages. Discover by filter with wiki_list (prefix, type, or tag; paged via offset with total and truncated in the result). Follow relationships with wiki_neighbors (a page's linked neighbourhood in one call) and wiki_backlinks. wiki_tags lists every tag with a count. wiki_recent lists pages by OKF timestamp, freshest first. wiki_search is keyword search: every whitespace-separated term must appear in the page (case-insensitive), so multi-word queries work; set regex=true for a pattern instead. Narrow search by type and tag. wiki_graph returns the whole page link graph.

This wiki cannot be written to over MCP.`

// Options configures the MCP server identity and OKF behavior.
type Options struct {
	Title        string // server name/identity; also shown in instructions
	Description  string // one-liner after the title
	Instructions string // optional CLAUDE.md schema appended to instructions
}

// New builds an MCP server backed by a read-only Core.
func New(c core.Core, opts Options) *mcp.Server {
	title := opts.Title
	if title == "" {
		title = "wiki"
	}
	desc := opts.Description
	if desc == "" {
		desc = "a read-only OKF knowledge base you can read, search, and navigate"
	}
	instructions := "This is " + title + ": " + desc + "." + instructionsSuffix
	if opts.Instructions != "" {
		instructions += "\n\n--- wiki schema (CLAUDE.md) ---\n\n" + opts.Instructions
	}

	s := mcp.NewServer(
		&mcp.Implementation{Name: title, Title: title, Version: version.Version},
		&mcp.ServerOptions{Instructions: instructions},
	)

	registerResources(s, c)
	registerTools(s, c, title, desc)
	return s
}

func slugFromURI(uri string) string {
	return strings.TrimPrefix(uri, uriPrefix)
}

func uriForSlug(slug string) string {
	return uriPrefix + slug
}

// registerResources wires the read template and a stateless resources/list.
func registerResources(s *mcp.Server, c core.Core) {
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		Name:        "page",
		Title:       "Wiki page",
		Description: "An OKF wiki page by slug, e.g. wiki://page/tables/customers. Returns the markdown body only. For navigation prefer the wiki_read tool, which also returns the parsed frontmatter (type, tags, ...) and the resolved inbound/outbound links; this resource is the plain prose.",
		MIMEType:    "text/markdown",
		URITemplate: uriPrefix + "{+slug}",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		slug := slugFromURI(req.Params.URI)
		page, err := c.Read(ctx, slug)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      req.Params.URI,
				MIMEType: "text/markdown",
				Text:     page.Body,
			}},
		}, nil
	})

	// Serve resources/list by re-walking the tree each call, so the list always
	// reflects the current filesystem with no cached snapshot (statelessness).
	// ponytail: no cursor pagination here — the list is frontmatter-only and
	// cheap; agents that need filtered/paged enumeration use the wiki_list tool.
	s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "resources/list" {
				return next(ctx, method, req)
			}
			res, err := c.List(ctx, "", "", "", 0, 0)
			if err != nil {
				return nil, err
			}
			out := &mcp.ListResourcesResult{Resources: make([]*mcp.Resource, 0, len(res.Items))}
			for _, m := range res.Items {
				out.Resources = append(out.Resources, &mcp.Resource{
					URI:         uriForSlug(m.Slug),
					Name:        m.Slug,
					Title:       m.Title,
					Description: m.Description,
					MIMEType:    "text/markdown",
				})
			}
			return out, nil
		}
	})
}

var readOnly = &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}

func registerTools(s *mcp.Server, c core.Core, title, description string) {
	type listIn struct {
		Prefix string `json:"prefix,omitempty" jsonschema:"only pages whose slug starts with this prefix, e.g. tables/"`
		Type   string `json:"type,omitempty" jsonschema:"filter to pages with this OKF type, e.g. BigQuery Table"`
		Tag    string `json:"tag,omitempty" jsonschema:"filter to pages carrying this tag"`
		Limit  int    `json:"limit,omitempty" jsonschema:"max pages to return (default 500)"`
		Offset int    `json:"offset,omitempty" jsonschema:"skip this many matching pages, for paging"`
	}
	type listOut struct {
		Pages     []core.PageMeta `json:"pages"`
		Total     int             `json:"total" jsonschema:"total pages matching the filters across the whole wiki"`
		Truncated bool            `json:"truncated" jsonschema:"true when more pages match than were returned; page with offset"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_list",
		Description: "List wiki pages (slug-sorted) with OKF metadata. Filter by prefix, type, or tag; page large wikis with offset (see total and truncated).",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
		res, err := c.List(ctx, in.Prefix, in.Type, in.Tag, in.Limit, in.Offset)
		if err != nil {
			return nil, listOut{}, err
		}
		return nil, listOut{Pages: res.Items, Total: res.Total, Truncated: res.Truncated}, nil
	})

	type readIn struct {
		Slug string `json:"slug" jsonschema:"slug of the page, e.g. tables/customers"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_read",
		Description: "Read a page by slug: its markdown body plus inlined outbound and inbound links (each with slug, title, and OKF type). Same data as the wiki://page/{slug} resource.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in readIn) (*mcp.CallToolResult, core.Page, error) {
		page, err := c.Read(ctx, in.Slug)
		if err != nil {
			return nil, core.Page{}, err
		}
		return nil, page, nil
	})

	type searchIn struct {
		Query string `json:"query" jsonschema:"keywords to find; every whitespace-separated term must appear in the page (case-insensitive)"`
		Regex bool   `json:"regex,omitempty" jsonschema:"treat query as a single regular expression (RE2) instead of keywords"`
		Type  string `json:"type,omitempty" jsonschema:"narrow to this OKF type"`
		Tag   string `json:"tag,omitempty" jsonschema:"narrow to this tag"`
		Limit int    `json:"limit,omitempty" jsonschema:"max hits to return (default 100)"`
	}
	type searchOut struct {
		Hits []core.Match `json:"hits"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_search",
		Description: "Keyword search over page bodies: every whitespace-separated term must appear (case-insensitive AND), or one RE2 pattern when regex=true. Optionally narrowed by type and tag. Deterministic slug ordering; each hit carries title, type, tags, and a context snippet.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
		hits, err := c.Search(ctx, in.Query, in.Regex, in.Type, in.Tag, in.Limit)
		if err != nil {
			return nil, searchOut{}, err
		}
		return nil, searchOut{Hits: orEmpty(hits)}, nil
	})

	type backlinksIn struct {
		Slug string `json:"slug" jsonschema:"slug of the page to find links to"`
	}
	type backlinksOut struct {
		Backlinks []string `json:"backlinks"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_backlinks",
		Description: "List slugs of pages that link to the given page (via [[wikilinks]] or relative .md links).",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in backlinksIn) (*mcp.CallToolResult, backlinksOut, error) {
		bl, err := c.Backlinks(ctx, in.Slug)
		if err != nil {
			return nil, backlinksOut{}, err
		}
		return nil, backlinksOut{Backlinks: orEmpty(bl)}, nil
	})

	type neighborsIn struct {
		Slug  string `json:"slug" jsonschema:"slug of the page whose neighbourhood to pull"`
		Depth int    `json:"depth,omitempty" jsonschema:"hops out from the page, 1-3 (default 1)"`
	}
	type neighborsOut struct {
		Neighbors []core.GraphNode `json:"neighbors"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_neighbors",
		Description: "Return a page's linked neighbourhood (undirected) within depth hops in one call, each neighbor with title, OKF type, and link degree.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in neighborsIn) (*mcp.CallToolResult, neighborsOut, error) {
		nb, err := c.Neighbors(ctx, in.Slug, in.Depth)
		if err != nil {
			return nil, neighborsOut{}, err
		}
		return nil, neighborsOut{Neighbors: orEmpty(nb)}, nil
	})

	type tagsIn struct {
		Tag string `json:"tag,omitempty" jsonschema:"a specific tag to get its count; omit to list all tags"`
	}
	type tagsOut struct {
		Tags []core.TagCount `json:"tags"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_tags",
		Description: "List every tag with its page count, or pass a tag to get just its count. Use for faceted navigation, then filter with wiki_list tag=.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in tagsIn) (*mcp.CallToolResult, tagsOut, error) {
		var tagPtr *string
		if in.Tag != "" {
			tagPtr = &in.Tag
		}
		tags, err := c.Tags(ctx, tagPtr)
		if err != nil {
			return nil, tagsOut{}, err
		}
		return nil, tagsOut{Tags: orEmpty(tags)}, nil
	})

	type hubsIn struct {
		Limit int `json:"limit,omitempty" jsonschema:"how many top pages to return (default 10)"`
	}
	type hubsOut struct {
		Hubs []core.GraphNode `json:"hubs"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_hubs",
		Description: "List the most-connected pages, highest link degree first. Good entry points for orienting in an unfamiliar wiki.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in hubsIn) (*mcp.CallToolResult, hubsOut, error) {
		hubs, err := c.Hubs(ctx, in.Limit)
		if err != nil {
			return nil, hubsOut{}, err
		}
		return nil, hubsOut{Hubs: orEmpty(hubs)}, nil
	})

	type recentIn struct {
		Limit int `json:"limit,omitempty" jsonschema:"how many pages to return, freshest first (default 20)"`
	}
	type recentOut struct {
		Pages []core.PageMeta `json:"pages"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_recent",
		Description: "List concept pages ordered by their OKF timestamp (knowledge last updated), freshest first. Use to see what has changed most recently or to gauge staleness.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in recentIn) (*mcp.CallToolResult, recentOut, error) {
		pages, err := c.Recent(ctx, in.Limit)
		if err != nil {
			return nil, recentOut{}, err
		}
		return nil, recentOut{Pages: orEmpty(pages)}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_graph",
		Description: "Return the whole page link graph: nodes (slug, title, OKF type, degree) and undirected edges from resolved links.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, core.Graph, error) {
		g, err := c.Graph(ctx)
		if err != nil {
			return nil, core.Graph{}, err
		}
		return nil, g, nil
	})

	type historyIn struct {
		Slug  string `json:"slug" jsonschema:"slug of the page"`
		Limit int    `json:"limit,omitempty" jsonschema:"max commits (default 20)"`
	}
	type historyOut struct {
		Versioned bool          `json:"versioned" jsonschema:"false when the wiki is not a git repo, so empty commits means unversioned rather than no changes"`
		Commits   []core.Commit `json:"commits"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_history",
		Description: "Git commit history for a page, newest first. If versioned is false the wiki is not a git repo, so an empty commits list means history is unavailable, not that the page is unchanged.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, historyOut, error) {
		commits, err := c.History(ctx, in.Slug, in.Limit)
		if err != nil {
			return nil, historyOut{}, err
		}
		return nil, historyOut{Versioned: versioned(c), Commits: orEmpty(commits)}, nil
	})

	// wiki_info lets an agent confirm which knowledge base it is connected to.
	type infoOut struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Pages       int    `json:"pages" jsonschema:"number of concept pages (reserved index/log files excluded)"`
		Versioned   bool   `json:"versioned"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "wiki_info",
		Description: "Identify the connected wiki: its title, description, concept-page count, and whether it is git-versioned. Call this to confirm you are querying the intended knowledge base.",
		Annotations: readOnly,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, infoOut, error) {
		res, err := c.List(ctx, "", "", "", 0, 0)
		if err != nil {
			return nil, infoOut{}, err
		}
		return nil, infoOut{Title: title, Description: description, Pages: res.Total, Versioned: versioned(c)}, nil
	})
}

// versioned reports whether the Core is backed by a git repo, when it exposes
// that capability.
func versioned(c core.Core) bool {
	if v, ok := c.(interface{ Versioned() bool }); ok {
		return v.Versioned()
	}
	return false
}

// orEmpty makes an empty MCP array serialize as [] rather than null.
func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ServeStdio runs an MCP server over stdin/stdout for an agent subprocess.
func ServeStdio(ctx context.Context, s *mcp.Server) error {
	return s.Run(ctx, &mcp.StdioTransport{})
}
