package store

import (
	"sort"
	"strings"
	"testing"
)

func TestTOMLFrontmatter(t *testing.T) {
	fm, body := SplitFrontmatter("+++\ntitle = \"Deploy Runbook\"\ntags = [\"ops\", \"runbook\"]\n+++\n# Body\nhi\n")
	if fm == nil || fm["title"] != "Deploy Runbook" {
		t.Fatalf("title = %v, want Deploy Runbook", fm["title"])
	}
	tags, _ := fm["tags"].([]any)
	if len(tags) != 2 || tags[0] != "ops" || tags[1] != "runbook" {
		t.Errorf("tags = %v, want [ops runbook]", fm["tags"])
	}
	if !strings.HasPrefix(body, "# Body") {
		t.Errorf("body = %q, want the +++ block stripped", body)
	}
}

func TestOKFFrontmatterExtraction(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(slug, content string) {
		if err := s.Write(slug, content, "", "m"); err != nil {
			t.Fatalf("write %s: %v", slug, err)
		}
	}
	write("tables/orders", "---\ntitle: Orders\ntype: BigQuery Table\ndescription: One row per order.\nresource: https://example.com/orders\ntags: [sales]\ntimestamp: 2026-06-01T00:00:00Z\n---\n\nBody.\n")
	write("metrics/wau", "---\ntitle: WAU\ntype: Metric\ndescription: Weekly active users.\nresource: https://example.com/wau\ntags: [kpi]\ntimestamp: 2026-06-01T00:00:00Z\n---\n\nBody.\n")
	write("plain", "---\ntitle: Plain\n---\n\nNo OKF fields.\n")

	pages, err := s.ListOKF()
	if err != nil {
		t.Fatalf("ListOKF: %v", err)
	}
	if len(pages) != 3 {
		t.Fatalf("ListOKF count = %d, want 3", len(pages))
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Slug < pages[j].Slug })

	bySlug := make(map[string]PageOKF, len(pages))
	for _, p := range pages {
		bySlug[p.Slug] = p
	}

	orders := bySlug["tables/orders"]
	if orders.Type != "BigQuery Table" {
		t.Errorf("orders.Type = %q, want BigQuery Table", orders.Type)
	}
	if orders.Description != "One row per order." {
		t.Errorf("orders.Description = %q", orders.Description)
	}
	if orders.Resource != "https://example.com/orders" {
		t.Errorf("orders.Resource = %q", orders.Resource)
	}
	if orders.Timestamp != "2026-06-01T00:00:00Z" {
		t.Errorf("orders.Timestamp = %q", orders.Timestamp)
	}
	if len(orders.Tags) != 1 || orders.Tags[0] != "sales" {
		t.Errorf("orders.Tags = %v", orders.Tags)
	}

	if wau := bySlug["metrics/wau"]; wau.Type != "Metric" {
		t.Errorf("wau.Type = %q, want Metric", wau.Type)
	}

	plain := bySlug["plain"]
	if plain.Type != "" || plain.Description != "" || plain.Resource != "" {
		t.Errorf("plain should have no OKF fields: %+v", plain)
	}
}

func TestOKFGraphNodeTypes(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(slug, content string) {
		if err := s.Write(slug, content, "", "m"); err != nil {
			t.Fatalf("write %s: %v", slug, err)
		}
	}
	write("tables/orders", "---\ntitle: Orders\ntype: BigQuery Table\n---\n\nBody.\n")
	write("metrics/wau", "---\ntitle: WAU\ntype: Metric\n---\n\nBody.\n")
	write("plain", "---\ntitle: Plain\n---\n\nBody.\n")

	g, err := s.GraphView()
	if err != nil {
		t.Fatalf("GraphView: %v", err)
	}
	typeBySlug := make(map[string]string, len(g.Nodes))
	for _, n := range g.Nodes {
		typeBySlug[n.Slug] = n.Type
	}
	if typeBySlug["tables/orders"] != "BigQuery Table" {
		t.Errorf("orders node type = %q, want BigQuery Table", typeBySlug["tables/orders"])
	}
	if typeBySlug["metrics/wau"] != "Metric" {
		t.Errorf("wau node type = %q, want Metric", typeBySlug["metrics/wau"])
	}
	if typeBySlug["plain"] != "" {
		t.Errorf("plain node type = %q, want empty", typeBySlug["plain"])
	}
}

func TestMarkdownLinkEdges(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := func(slug, content string) {
		if err := s.Write(slug, content, "", "m"); err != nil {
			t.Fatal(err)
		}
	}
	w("a", "---\ntitle: A\n---\nsee [the setup](b), an [external](https://x.com), an [image](pic.png)\n")
	w("b", "---\ntitle: B\n---\nx\n")

	in, err := s.Backlinks("b")
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 || in[0].Slug != "a" {
		t.Fatalf("backlinks(b) via plain markdown link = %+v, want [a]", in)
	}
	// External and image markdown links must not create broken-link noise.
	if h, _ := s.Health(); len(h.Broken) != 0 {
		t.Errorf("markdown links should not produce broken links, got %+v", h.Broken)
	}
}
