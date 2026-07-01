package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/msradam/waqwaq/core"
)

// connect builds a Core over a fixture wiki, serves it over an in-memory
// transport, and returns a connected client session.
func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, "wiki", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.md", "---\ntitle: Home\ntype: Index\n---\nSee [[tables/customers]].\n")
	write("tables/customers.md", "---\ntitle: Customers\ntype: BigQuery Table\ntags: [sales]\n---\nLinks [[tables/orders]].\n")
	write("tables/orders.md", "---\ntitle: Orders\ntype: BigQuery Table\ntags: [sales]\n---\nJoins [[tables/customers]].\n")

	c, err := core.New(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(c, Options{Title: "TestWiki", Description: "a test wiki"})

	serverT, clientT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestResourcesList(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListResources(context.Background(), &mcp.ListResourcesParams{})
	if err != nil {
		t.Fatal(err)
	}
	// index.md is reserved (navigation, not a concept), so resources enumerate
	// the 2 concept pages: customers and orders.
	if len(res.Resources) != 2 {
		t.Fatalf("want 2 resources, got %d", len(res.Resources))
	}
	got := map[string]bool{}
	for _, r := range res.Resources {
		got[r.URI] = true
		if r.MIMEType != "text/markdown" {
			t.Fatalf("resource %s wrong mime %q", r.URI, r.MIMEType)
		}
	}
	if !got["wiki://page/tables/customers"] {
		t.Fatalf("missing expected resource URI, got %v", got)
	}
}

func TestResourceRead(t *testing.T) {
	cs := connect(t)
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "wiki://page/tables/customers"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("want 1 content, got %d", len(res.Contents))
	}
	if !strings.Contains(res.Contents[0].Text, "Links") {
		t.Fatalf("resource body missing expected text: %q", res.Contents[0].Text)
	}
}

// callTool calls a tool and unmarshals its structured content into out.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("tool %s returned error: %+v", name, res.Content)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("unmarshal %s output: %v", name, err)
	}
}

func TestToolWikiList(t *testing.T) {
	cs := connect(t)
	var out struct {
		Pages     []core.PageMeta `json:"pages"`
		Total     int             `json:"total"`
		Truncated bool            `json:"truncated"`
	}
	callTool(t, cs, "wiki_list", map[string]any{"type": "BigQuery Table"}, &out)
	if out.Total != 2 {
		t.Fatalf("want 2 BigQuery Table pages, got %d", out.Total)
	}
}

func TestToolWikiReadInlineEdges(t *testing.T) {
	cs := connect(t)
	var page core.Page
	callTool(t, cs, "wiki_read", map[string]any{"slug": "tables/customers"}, &page)
	if len(page.Outbound) == 0 || page.Outbound[0].Slug != "tables/orders" {
		t.Fatalf("expected outbound to orders, got %+v", page.Outbound)
	}
	if len(page.Inbound) == 0 {
		t.Fatalf("expected inbound edges, got none")
	}
}

func TestToolWikiSearch(t *testing.T) {
	cs := connect(t)
	var out struct {
		Hits []core.Match `json:"hits"`
	}
	callTool(t, cs, "wiki_search", map[string]any{"query": "Joins"}, &out)
	if len(out.Hits) != 1 || out.Hits[0].Slug != "tables/orders" {
		t.Fatalf("search: want orders, got %+v", out.Hits)
	}
}

func TestToolWikiHubs(t *testing.T) {
	cs := connect(t)
	var out struct {
		Hubs []core.GraphNode `json:"hubs"`
	}
	callTool(t, cs, "wiki_hubs", map[string]any{"limit": 1}, &out)
	if len(out.Hubs) != 1 || out.Hubs[0].Slug != "tables/customers" {
		t.Fatalf("top hub should be customers, got %+v", out.Hubs)
	}
}

func TestToolWikiInfo(t *testing.T) {
	cs := connect(t)
	var out struct {
		Title     string `json:"title"`
		Pages     int    `json:"pages"`
		Versioned bool   `json:"versioned"`
	}
	callTool(t, cs, "wiki_info", map[string]any{}, &out)
	if out.Title != "TestWiki" {
		t.Fatalf("wiki_info title = %q, want TestWiki", out.Title)
	}
	if out.Pages != 2 { // index reserved; customers + orders are concepts
		t.Fatalf("wiki_info pages = %d, want 2", out.Pages)
	}
}

func TestToolWikiHistoryVersioned(t *testing.T) {
	cs := connect(t)
	var out struct {
		Versioned bool          `json:"versioned"`
		Commits   []core.Commit `json:"commits"`
	}
	callTool(t, cs, "wiki_history", map[string]any{"slug": "index"}, &out)
	// The fixture is a bare temp dir, not a git repo.
	if out.Versioned {
		t.Fatal("non-git fixture should report versioned=false")
	}
	if len(out.Commits) != 0 {
		t.Fatalf("want no commits, got %d", len(out.Commits))
	}
}

func TestHubsAndGraphTitles(t *testing.T) {
	cs := connect(t)
	// Hubs must exclude reserved index.md and never emit an empty title.
	var hubs struct {
		Hubs []core.GraphNode `json:"hubs"`
	}
	callTool(t, cs, "wiki_hubs", map[string]any{"limit": 10}, &hubs)
	for _, h := range hubs.Hubs {
		if h.Slug == "index" {
			t.Fatal("wiki_hubs must exclude reserved index.md")
		}
		if h.Title == "" {
			t.Fatalf("hub %q has empty title", h.Slug)
		}
	}
	// The full graph keeps index as a node, but with a non-empty (fallback) title.
	var g core.Graph
	callTool(t, cs, "wiki_graph", map[string]any{}, &g)
	for _, n := range g.Nodes {
		if n.Title == "" {
			t.Fatalf("graph node %q has empty title", n.Slug)
		}
	}
}

func TestToolWikiRecent(t *testing.T) {
	cs := connect(t)
	var out struct {
		Pages []core.PageMeta `json:"pages"`
	}
	callTool(t, cs, "wiki_recent", map[string]any{"limit": 5}, &out)
	// Fixture pages carry no timestamp, so this just confirms the tool returns the
	// concept pages without error and excludes the reserved index.
	if len(out.Pages) != 2 {
		t.Fatalf("wiki_recent: want 2 concept pages, got %d", len(out.Pages))
	}
	for _, p := range out.Pages {
		if p.Slug == "index" {
			t.Fatal("wiki_recent must exclude reserved index.md")
		}
	}
}

func TestNoMutationTools(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if strings.Contains(tool.Name, "write") || strings.Contains(tool.Name, "delete") ||
			strings.Contains(tool.Name, "ingest") || strings.Contains(tool.Name, "edit") {
			t.Fatalf("read-only baseline must expose no mutation tool, found %q", tool.Name)
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool %q missing readOnlyHint", tool.Name)
		}
	}
}
