package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
