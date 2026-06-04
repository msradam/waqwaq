package kbclient

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientDecodesAPI(t *testing.T) {
	mux := http.NewServeMux()
	json := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}
	}
	mux.HandleFunc("/api/pages", json(`{"pages":[{"slug":"a","title":"Alpha"},{"slug":"b","title":"Beta"}]}`))
	mux.HandleFunc("/api/page/a", json(`{"slug":"a","title":"Alpha","frontmatter":{"title":"Alpha"},"body":"# Alpha"}`))
	mux.HandleFunc("/api/hubs", json(`{"hubs":[{"slug":"a","title":"Alpha","degree":3}]}`))
	mux.HandleFunc("/api/path", json(`{"path":[{"slug":"a","title":"Alpha"},{"slug":"b","title":"Beta"}]}`))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	c := New(ts.URL, "")
	if pages, err := c.List(); err != nil || len(pages) != 2 || pages[0].Slug != "a" || pages[1].Title != "Beta" {
		t.Fatalf("List = %+v, %v", pages, err)
	}
	if page, err := c.Read("a"); err != nil || page.Title != "Alpha" || page.Frontmatter["title"] != "Alpha" {
		t.Fatalf("Read = %+v, %v", page, err)
	}
	if hubs, err := c.Hubs(10); err != nil || len(hubs) != 1 || hubs[0].Degree != 3 {
		t.Fatalf("Hubs = %+v, %v", hubs, err)
	}
	if path, err := c.Path("a", "b"); err != nil || len(path) != 2 || path[1].Slug != "b" {
		t.Fatalf("Path = %+v, %v", path, err)
	}
}
