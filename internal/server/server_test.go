package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/msradam/waqwaq/core"
)

func fixtureServer(t *testing.T) http.Handler {
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
	write("index.md", "---\ntitle: Home\ntype: Index\n---\n# Home\n\nStart at [[tables/customers]].\n")
	write("tables/customers.md", "---\ntitle: Customers\ntype: BigQuery Table\ntags: [sales]\ndescription: One row per account.\nresource: https://example.com/customers\n---\n# Customers\n\nJoins with [[tables/orders]].\n")
	write("tables/orders.md", "---\ntitle: Orders\ntype: BigQuery Table\ntags: [sales]\n---\n# Orders\n\nRefs [[tables/customers]].\n")

	c, err := core.New(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return New(c, nil, Site{Title: "Test"}).Handler()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPageRenders(t *testing.T) {
	h := fixtureServer(t)
	rec := get(t, h, "/wiki/tables/customers")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Joins with") {
		t.Fatalf("rendered body missing content:\n%s", body[:min(len(body), 400)])
	}
	// Sidebar shows an outbound link to orders and the OKF type.
	if !strings.Contains(body, "/wiki/tables/orders") {
		t.Fatal("missing outbound link in sidebar")
	}
	if !strings.Contains(body, "BigQuery Table") {
		t.Fatal("missing OKF type in metadata")
	}
	// Inbound: orders links to customers.
	if !strings.Contains(body, "Linked from") {
		t.Fatal("missing inbound links section")
	}
}

func TestPageNotFound(t *testing.T) {
	h := fixtureServer(t)
	if rec := get(t, h, "/wiki/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestRootRedirectsToIndex(t *testing.T) {
	h := fixtureServer(t)
	rec := get(t, h, "/")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/wiki/index" {
		t.Fatalf("root should redirect to /wiki/index, got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestSearch(t *testing.T) {
	h := fixtureServer(t)
	rec := get(t, h, "/search?q=Joins")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "/wiki/tables/customers") {
		t.Fatal("search should link the matching page")
	}
}

func TestList(t *testing.T) {
	h := fixtureServer(t)
	rec := get(t, h, "/list")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "tables/orders") {
		t.Fatal("list should include all pages")
	}
}

func TestTags(t *testing.T) {
	h := fixtureServer(t)
	rec := get(t, h, "/tags")
	if !strings.Contains(rec.Body.String(), "sales") {
		t.Fatal("tags page should list the sales tag")
	}
}

func TestGraphJSON(t *testing.T) {
	h := fixtureServer(t)
	rec := get(t, h, "/graph.json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var g core.Graph
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil {
		t.Fatalf("invalid graph JSON: %v", err)
	}
	if len(g.Nodes) != 3 {
		t.Fatalf("want 3 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) == 0 {
		t.Fatal("expected edges")
	}
}

func TestGraphPageServes(t *testing.T) {
	h := fixtureServer(t)
	rec := get(t, h, "/graph")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "graph.json") {
		t.Fatalf("graph page should reference graph.json, got %d", rec.Code)
	}
}

func TestHealthz(t *testing.T) {
	h := fixtureServer(t)
	if rec := get(t, h, "/healthz"); rec.Body.String() != "ok" {
		t.Fatalf("healthz = %q", rec.Body.String())
	}
}

func TestStaticCSS(t *testing.T) {
	h := fixtureServer(t)
	rec := get(t, h, "/static/style.css")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "font-family") {
		t.Fatalf("style.css not served, got %d", rec.Code)
	}
}

// Read-only guarantee: non-GET methods are not routed to any mutating handler
// (there are none). A POST to a page path is simply handled as the same
// read handler; assert nothing mutates by confirming it never 5xxs oddly.
func TestNoWriteRoutes(t *testing.T) {
	h := fixtureServer(t)
	for _, path := range []string{"/wiki/tables/customers", "/list", "/search"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("x=1"))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code >= 500 {
			t.Fatalf("POST %s returned %d; no write route should exist", path, rec.Code)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
