package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fixture builds a small wiki on disk and returns a Core over it.
func fixture(t *testing.T, okf bool) Core {
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
	write("index.md", "---\ntitle: Home\n---\nStart at [[tables/customers]].\n")
	write("tables/customers.md", "---\ntitle: Customers\ntype: BigQuery Table\ntags: [sales, identity]\ndescription: One row per account.\nresource: https://example.com/customers\ntimestamp: 2026-06-01T00:00:00Z\n---\nPart of [[datasets/sales]]. See [[tables/orders]].\n")
	write("tables/orders.md", "---\ntitle: Orders\ntype: BigQuery Table\ntags: [sales]\n---\nJoins [[tables/customers]] on id.\n")
	write("datasets/sales.md", "---\ntitle: Sales\ntype: Dataset\ntags: [sales]\n---\nHas [[tables/customers]] and [[tables/orders]].\n")

	c, err := New(dir, okf)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestListAndFilters(t *testing.T) {
	c := fixture(t, false)
	ctx := context.Background()

	all, err := c.List(ctx, "", "", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// index.md is reserved (navigation, not a concept), so the catalog is the 3
	// concept pages: customers, orders, sales.
	if all.Total != 3 {
		t.Fatalf("want 3 concept pages, got %d", all.Total)
	}
	for _, p := range all.Items {
		if p.Slug == "index" {
			t.Fatal("reserved index.md must not appear in the concept list")
		}
	}

	tables, _ := c.List(ctx, "", "BigQuery Table", "", 0, 0)
	if tables.Total != 2 {
		t.Fatalf("type filter: want 2, got %d", tables.Total)
	}
	// Type matching is case-insensitive (producer-chosen strings, agent-friendly).
	lower, _ := c.List(ctx, "", "bigquery table", "", 0, 0)
	if lower.Total != 2 {
		t.Fatalf("case-insensitive type filter: want 2, got %d", lower.Total)
	}

	byPrefix, _ := c.List(ctx, "tables/", "", "", 0, 0)
	if byPrefix.Total != 2 {
		t.Fatalf("prefix filter: want 2, got %d", byPrefix.Total)
	}

	byTag, _ := c.List(ctx, "", "", "identity", 0, 0)
	if byTag.Total != 1 || byTag.Items[0].Slug != "tables/customers" {
		t.Fatalf("tag filter: want customers, got %+v", byTag.Items)
	}
}

func TestListDefaultLimitVsAll(t *testing.T) {
	// With more than the 500 default, a caller that wants everything must ask for
	// it; the default caps the page but Total always reflects the full count.
	c := genWiki(t, 600)
	ctx := context.Background()

	def, _ := c.List(ctx, "", "", "", 0, 0) // limit 0 -> default 500
	if def.Total != 600 || len(def.Items) != 500 || !def.Truncated {
		t.Fatalf("default: total=%d items=%d trunc=%v, want 600/500/true", def.Total, len(def.Items), def.Truncated)
	}
	all, _ := c.List(ctx, "", "", "", 1<<30, 0) // explicit large limit -> everything
	if all.Total != 600 || len(all.Items) != 600 || all.Truncated {
		t.Fatalf("all: total=%d items=%d trunc=%v, want 600/600/false", all.Total, len(all.Items), all.Truncated)
	}
}

func TestListPagination(t *testing.T) {
	c := fixture(t, false)
	ctx := context.Background()
	// 3 concept pages (index is reserved), paged 2 + 1.
	page1, _ := c.List(ctx, "", "", "", 2, 0)
	if len(page1.Items) != 2 || !page1.Truncated || page1.Total != 3 {
		t.Fatalf("page1 wrong: len=%d trunc=%v total=%d", len(page1.Items), page1.Truncated, page1.Total)
	}
	page2, _ := c.List(ctx, "", "", "", 2, 2)
	if len(page2.Items) != 1 || page2.Truncated {
		t.Fatalf("page2 wrong: len=%d trunc=%v", len(page2.Items), page2.Truncated)
	}
	if page1.Items[0].Slug == page2.Items[0].Slug {
		t.Fatal("pages overlap")
	}
}

func TestReadOKFMetadata(t *testing.T) {
	c := fixture(t, false)
	p, err := c.Read(context.Background(), "tables/customers")
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "Customers" {
		t.Fatalf("title: %q", p.Title)
	}
	if p.Frontmatter["type"] != "BigQuery Table" {
		t.Fatalf("type: %v", p.Frontmatter["type"])
	}
	if p.Body == "" {
		t.Fatal("body empty")
	}
}

func TestReadOKFModeRequiresType(t *testing.T) {
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
	// index.md is reserved: no type, must be EXEMPT (spec conformance).
	write("index.md", "# Home\nno frontmatter at all\n")
	// A non-reserved concept page missing type must be rejected.
	write("orphan.md", "---\ntitle: Orphan\n---\nno type\n")
	// A typed concept page must read fine.
	write("tables/orders.md", "---\ntitle: Orders\ntype: BigQuery Table\n---\nbody\n")

	c, err := New(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if _, err := c.Read(ctx, "index"); err != nil {
		t.Fatalf("reserved index.md must be exempt from type rule, got: %v", err)
	}
	if _, err := c.Read(ctx, "orphan"); err == nil {
		t.Fatal("expected error reading typeless concept page in OKF mode")
	}
	if _, err := c.Read(ctx, "tables/orders"); err != nil {
		t.Fatalf("typed page should read in OKF mode: %v", err)
	}
}

func TestReadInlineEdges(t *testing.T) {
	c := fixture(t, false)
	p, err := c.Read(context.Background(), "tables/customers")
	if err != nil {
		t.Fatal(err)
	}
	// customers links out to datasets/sales and tables/orders.
	out := map[string]EdgeRef{}
	for _, e := range p.Outbound {
		out[e.Slug] = e
	}
	if _, ok := out["datasets/sales"]; !ok {
		t.Fatalf("missing outbound to datasets/sales: %+v", p.Outbound)
	}
	if out["tables/orders"].Type != "BigQuery Table" {
		t.Fatalf("outbound edge should carry type, got %+v", out["tables/orders"])
	}
	// customers is linked from index, orders, sales.
	in := map[string]bool{}
	for _, e := range p.Inbound {
		in[e.Slug] = true
	}
	for _, want := range []string{"index", "tables/orders", "datasets/sales"} {
		if !in[want] {
			t.Fatalf("missing inbound from %q: %+v", want, p.Inbound)
		}
	}
}

func TestRelativeMarkdownLinks(t *testing.T) {
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
	// Real OKF style: relative .md links, not [[wikilinks]].
	write("tables/orders.md", "---\ntitle: Orders\ntype: BigQuery Table\n---\nJoins [Customers](../tables/customers.md) and belongs to [Sales](../datasets/sales.md).\n")
	write("tables/customers.md", "---\ntitle: Customers\ntype: BigQuery Table\n---\nbody\n")
	write("datasets/sales.md", "---\ntitle: Sales\ntype: Dataset\n---\nbody\n")

	c, err := New(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	bl, err := c.Backlinks(context.Background(), "tables/customers")
	if err != nil {
		t.Fatal(err)
	}
	if len(bl) != 1 || bl[0] != "tables/orders" {
		t.Fatalf("relative .md link should produce a backlink, got %v", bl)
	}
}

func TestBacklinks(t *testing.T) {
	c := fixture(t, false)
	bl, err := c.Backlinks(context.Background(), "tables/customers")
	if err != nil {
		t.Fatal(err)
	}
	// index, orders, datasets/sales all link to customers.
	want := map[string]bool{"index": true, "tables/orders": true, "datasets/sales": true}
	if len(bl) != 3 {
		t.Fatalf("want 3 backlinks, got %v", bl)
	}
	for _, s := range bl {
		if !want[s] {
			t.Fatalf("unexpected backlink %q", s)
		}
	}
}

func TestSearch(t *testing.T) {
	c := fixture(t, false)
	res, err := c.Search(context.Background(), "Joins", false, "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Slug != "tables/orders" {
		t.Fatalf("search: want orders, got %+v", res)
	}
	if res[0].Type != "BigQuery Table" {
		t.Fatalf("search hit should carry OKF type, got %q", res[0].Type)
	}

	// Type filter narrows results.
	typed, _ := c.Search(context.Background(), "tables", false, "Dataset", "", 10)
	for _, m := range typed {
		if m.Type != "Dataset" {
			t.Fatalf("type filter leaked %q", m.Type)
		}
	}
}

func TestNeighbors(t *testing.T) {
	c := fixture(t, false)
	nb, err := c.Neighbors(context.Background(), "tables/customers", 1)
	if err != nil {
		t.Fatal(err)
	}
	// customers links to sales + orders; is linked from index, orders, sales.
	got := map[string]bool{}
	for _, n := range nb {
		got[n.Slug] = true
	}
	for _, want := range []string{"datasets/sales", "tables/orders", "index"} {
		if !got[want] {
			t.Fatalf("neighbor %q missing from %v", want, got)
		}
	}
}

func TestTags(t *testing.T) {
	c := fixture(t, false)
	tags, err := c.Tags(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, tc := range tags {
		counts[tc.Tag] = tc.Count
	}
	if counts["sales"] != 3 {
		t.Fatalf("sales tag: want 3, got %d", counts["sales"])
	}
	if counts["identity"] != 1 {
		t.Fatalf("identity tag: want 1, got %d", counts["identity"])
	}
}

func TestRecent(t *testing.T) {
	c := fixture(t, false)
	// Fixture: customers has a timestamp; orders/sales do not; index is reserved.
	pages, err := c.Recent(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 {
		t.Fatalf("recent should return the 3 concept pages, got %d", len(pages))
	}
	if pages[0].Slug != "tables/customers" {
		t.Fatalf("timestamped page should sort first, got %q", pages[0].Slug)
	}
	// The two timestamp-less pages come last, in slug order.
	if pages[1].Slug != "datasets/sales" || pages[2].Slug != "tables/orders" {
		t.Fatalf("timestamp-less pages should trail in slug order, got %q, %q", pages[1].Slug, pages[2].Slug)
	}
}

func TestHubs(t *testing.T) {
	c := fixture(t, false)
	hubs, err := c.Hubs(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(hubs) != 2 {
		t.Fatalf("want 2 hubs, got %d", len(hubs))
	}
	// customers is the most-connected node.
	if hubs[0].Slug != "tables/customers" {
		t.Fatalf("top hub should be customers, got %q (degree %d)", hubs[0].Slug, hubs[0].Degree)
	}
}

func TestGraph(t *testing.T) {
	c := fixture(t, false)
	g, err := c.Graph(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 4 {
		t.Fatalf("want 4 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) == 0 {
		t.Fatal("expected edges")
	}
	// Every edge endpoint that is a real page should appear in nodes.
	nodeSet := map[string]bool{}
	for _, n := range g.Nodes {
		nodeSet[n.Slug] = true
	}
	for _, e := range g.Edges {
		if !nodeSet[e.From] {
			t.Fatalf("edge from non-node %q", e.From)
		}
	}
}

func TestCacheReflectsChanges(t *testing.T) {
	dir := t.TempDir()
	wiki := filepath.Join(dir, "wiki")
	if err := os.MkdirAll(wiki, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(wiki, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.md", "---\ntitle: A\ntype: Note\n---\nbody\n")

	// refresh=0 forces a fresh stat-walk on every call, so changes are seen
	// immediately (the default TTL would hide them for up to a second).
	c, err := New(dir, false, WithRefreshInterval(0))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if r, _ := c.List(ctx, "", "", "", 0, 0); r.Total != 1 {
		t.Fatalf("want 1 page, got %d", r.Total)
	}
	// Add a page; the cache must pick it up.
	write("b.md", "---\ntitle: B\ntype: Note\n---\nlinks [[a]]\n")
	if r, _ := c.List(ctx, "", "", "", 0, 0); r.Total != 2 {
		t.Fatalf("after add: want 2 pages, got %d", r.Total)
	}
	// The new page's link must appear as a backlink to a.
	bl, _ := c.Backlinks(ctx, "a")
	if len(bl) != 1 || bl[0] != "b" {
		t.Fatalf("after add: want backlink from b, got %v", bl)
	}
	// Remove a page; the cache must drop it.
	if err := os.Remove(filepath.Join(wiki, "b.md")); err != nil {
		t.Fatal(err)
	}
	if r, _ := c.List(ctx, "", "", "", 0, 0); r.Total != 1 {
		t.Fatalf("after remove: want 1 page, got %d", r.Total)
	}
}

func TestHistoryNonGitDirIsEmpty(t *testing.T) {
	c := fixture(t, false)
	h, err := c.History(context.Background(), "index", 10)
	if err != nil {
		t.Fatalf("history on non-git wiki should be empty, not error: %v", err)
	}
	if len(h) != 0 {
		t.Fatalf("want empty history, got %d", len(h))
	}
}
