package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/msradam/waqwaq/internal/auth"
	"github.com/msradam/waqwaq/internal/review"
	"github.com/msradam/waqwaq/internal/store"
)

func testSession(t *testing.T, pages map[string]string) *mcp.ClientSession {
	t.Helper()
	dir := t.TempDir()
	for slug, content := range pages {
		path := filepath.Join(dir, "wiki", slug+".md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	q, err := review.New(st, "")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := auth.Load(filepath.Join(dir, ".waqwaq", "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	srv := New(st, q, reg, Options{})

	ct, srvT := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, srvT, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callJSON(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, out any) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if res.IsError {
		t.Fatalf("%s returned a tool error: %v", tool, res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatal(err)
	}
}

func page(title, tags string) string {
	return fmt.Sprintf("---\ntitle: %s\ntags: [%s]\n---\n\n# %s\n\nBody.\n", title, tags, title)
}

func TestWikiListPaging(t *testing.T) {
	pages := map[string]string{}
	for i := 0; i < 7; i++ {
		pages[fmt.Sprintf("a/p%d", i)] = page(fmt.Sprintf("A%d", i), "x")
	}
	pages["b/solo"] = page("Solo", "y")
	cs := testSession(t, pages)

	type listResult struct {
		Pages []struct {
			Slug  string `json:"slug"`
			Title string `json:"title"`
		} `json:"pages"`
		Total     int  `json:"total"`
		Truncated bool `json:"truncated"`
	}
	var all listResult
	callJSON(t, cs, "wiki_list", nil, &all)
	if all.Total != 8 || len(all.Pages) != 8 || all.Truncated {
		t.Fatalf("unfiltered list: total=%d pages=%d truncated=%v", all.Total, len(all.Pages), all.Truncated)
	}

	var pre listResult
	callJSON(t, cs, "wiki_list", map[string]any{"prefix": "a/"}, &pre)
	if pre.Total != 7 || len(pre.Pages) != 7 {
		t.Fatalf("prefix list: total=%d pages=%d", pre.Total, len(pre.Pages))
	}

	var lim listResult
	callJSON(t, cs, "wiki_list", map[string]any{"prefix": "a/", "limit": 3}, &lim)
	if lim.Total != 7 || len(lim.Pages) != 3 || !lim.Truncated {
		t.Fatalf("limited list: total=%d pages=%d truncated=%v", lim.Total, len(lim.Pages), lim.Truncated)
	}
	if lim.Pages[0].Slug != "a/p0" {
		t.Fatalf("first page = %q", lim.Pages[0].Slug)
	}

	var off listResult
	callJSON(t, cs, "wiki_list", map[string]any{"prefix": "a/", "limit": 3, "offset": 6}, &off)
	if off.Total != 7 || len(off.Pages) != 1 || off.Truncated {
		t.Fatalf("offset list: total=%d pages=%d truncated=%v", off.Total, len(off.Pages), off.Truncated)
	}
	if off.Pages[0].Slug != "a/p6" {
		t.Fatalf("offset page = %q", off.Pages[0].Slug)
	}
}

func TestWikiTagsCountsAndDrillDown(t *testing.T) {
	cs := testSession(t, map[string]string{
		"one":   page("One", "infra, api"),
		"two":   page("Two", "infra"),
		"three": page("Three", "api"),
	})

	var counts struct {
		Tags []struct {
			Tag   string `json:"tag"`
			Count int    `json:"count"`
		} `json:"tags"`
		Pages []any `json:"pages"`
	}
	callJSON(t, cs, "wiki_tags", nil, &counts)
	if len(counts.Tags) != 2 || len(counts.Pages) != 0 {
		t.Fatalf("tag counts: %+v", counts)
	}
	if counts.Tags[0].Tag != "api" || counts.Tags[0].Count != 2 || counts.Tags[1].Tag != "infra" || counts.Tags[1].Count != 2 {
		t.Fatalf("tag counts: %+v", counts.Tags)
	}

	type tagPages struct {
		Pages []struct {
			Slug string `json:"slug"`
		} `json:"pages"`
	}
	var infra tagPages
	callJSON(t, cs, "wiki_tags", map[string]any{"tag": "infra"}, &infra)
	if len(infra.Pages) != 2 {
		t.Fatalf("infra pages: %+v", infra.Pages)
	}
	var unknown tagPages
	callJSON(t, cs, "wiki_tags", map[string]any{"tag": "nope"}, &unknown)
	if unknown.Pages == nil || len(unknown.Pages) != 0 {
		t.Fatalf("unknown tag pages: %+v", unknown.Pages)
	}
}

func TestEmptyResultsSerializeAsArrays(t *testing.T) {
	cs := testSession(t, map[string]string{
		"index": "---\ntitle: Index\n---\n\n# Index\n\nNo links here.\n",
	})
	for tool, args := range map[string]map[string]any{
		"wiki_health":    nil,
		"wiki_backlinks": {"slug": "index"},
		"wiki_neighbors": {"slug": "index"},
		"wiki_hubs":      nil,
		"wiki_list_raw":  nil,
		"wiki_search":    {"query": "nosuchwordanywhere"},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: err=%v isError=%v", tool, err, res != nil && res.IsError)
		}
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "null") {
			t.Errorf("%s emits null for an empty list: %s", tool, raw)
		}
	}
}

func okfPage(title, okfType, description, resource string) string {
	return fmt.Sprintf("---\ntitle: %s\ntype: %s\ndescription: %s\nresource: %s\ntags: [data]\ntimestamp: 2026-06-01T00:00:00Z\n---\n\n# %s\n", title, okfType, description, resource, title)
}

func TestOKFListAndTypeFilter(t *testing.T) {
	cs := testSession(t, map[string]string{
		"datasets/sales":   okfPage("Sales Dataset", "Dataset", "Order data", "https://example.com/sales"),
		"tables/orders":    okfPage("Orders", "BigQuery Table", "One row per order", "https://example.com/orders"),
		"tables/customers": okfPage("Customers", "BigQuery Table", "Customer master", "https://example.com/customers"),
		"metrics/wau":      okfPage("Weekly Active Users", "Metric", "WAU definition", "https://example.com/wau"),
	})

	type okfEntry struct {
		Slug        string   `json:"slug"`
		Title       string   `json:"title"`
		Type        string   `json:"type"`
		Description string   `json:"description"`
		Resource    string   `json:"resource"`
		Tags        []string `json:"tags"`
		Timestamp   string   `json:"timestamp"`
	}
	type listResult struct {
		Pages     []okfEntry `json:"pages"`
		Total     int        `json:"total"`
		Truncated bool       `json:"truncated"`
	}

	// All pages include OKF fields.
	var all listResult
	callJSON(t, cs, "wiki_list", nil, &all)
	if all.Total != 4 {
		t.Fatalf("total = %d, want 4", all.Total)
	}
	for _, p := range all.Pages {
		if p.Type == "" {
			t.Errorf("page %q missing type", p.Slug)
		}
		if p.Resource == "" {
			t.Errorf("page %q missing resource", p.Slug)
		}
		if p.Timestamp == "" {
			t.Errorf("page %q missing timestamp", p.Slug)
		}
	}

	// Type filter: only BigQuery Table pages.
	var tables listResult
	callJSON(t, cs, "wiki_list", map[string]any{"type": "BigQuery Table"}, &tables)
	if tables.Total != 2 {
		t.Fatalf("type filter total = %d, want 2", tables.Total)
	}
	for _, p := range tables.Pages {
		if p.Type != "BigQuery Table" {
			t.Errorf("type filter returned %q with type %q", p.Slug, p.Type)
		}
	}

	// Type filter is case-insensitive.
	var lower listResult
	callJSON(t, cs, "wiki_list", map[string]any{"type": "bigquery table"}, &lower)
	if lower.Total != 2 {
		t.Fatalf("case-insensitive type filter total = %d, want 2", lower.Total)
	}

	// Type filter + prefix together.
	var narrow listResult
	callJSON(t, cs, "wiki_list", map[string]any{"type": "Dataset", "prefix": "datasets/"}, &narrow)
	if narrow.Total != 1 || narrow.Pages[0].Slug != "datasets/sales" {
		t.Fatalf("type+prefix filter: %+v", narrow)
	}
}

func TestOKFGraphNodesIncludeType(t *testing.T) {
	cs := testSession(t, map[string]string{
		"tables/orders": okfPage("Orders", "BigQuery Table", "One row per order", "https://example.com/orders"),
		"metrics/wau":   okfPage("WAU", "Metric", "Weekly active users", "https://example.com/wau"),
		"plain":         "---\ntitle: Plain\n---\n\nNo OKF type.\n",
	})

	type node struct {
		Slug   string `json:"slug"`
		Type   string `json:"type"`
		Degree int    `json:"degree"`
	}
	var result struct {
		Pages []node `json:"pages"`
	}
	callJSON(t, cs, "wiki_graph", nil, &result)

	typeBySlug := make(map[string]string, len(result.Pages))
	for _, p := range result.Pages {
		typeBySlug[p.Slug] = p.Type
	}
	if typeBySlug["tables/orders"] != "BigQuery Table" {
		t.Errorf("orders type = %q, want BigQuery Table", typeBySlug["tables/orders"])
	}
	if typeBySlug["metrics/wau"] != "Metric" {
		t.Errorf("wau type = %q, want Metric", typeBySlug["metrics/wau"])
	}
	if typeBySlug["plain"] != "" {
		t.Errorf("plain type = %q, want empty", typeBySlug["plain"])
	}
}
