package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/msradam/waqwaq/internal/store"
)

// The /api routes expose the same reads as the MCP tools as plain JSON, for
// non-MCP clients (CI jobs, dashboards, scripts). They follow the same access
// rules as the rest of the web UI.

type apiRef struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

func (s *Server) registerAPI(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/pages", s.apiPages)
	mux.HandleFunc("GET /api/page/{slug...}", s.apiPage)
	mux.HandleFunc("GET /api/search", s.apiSearch)
	mux.HandleFunc("GET /api/graph", s.apiGraph)
	mux.HandleFunc("GET /api/neighbors/{slug...}", s.apiNeighbors)
	mux.HandleFunc("GET /api/path", s.apiPath)
	mux.HandleFunc("GET /api/hubs", s.apiHubs)
	mux.HandleFunc("GET /api/health", s.apiHealth)
	mux.HandleFunc("GET /api/backlinks/{slug...}", s.apiBacklinks)
	mux.HandleFunc("GET /api/tags", s.apiTags)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func refs(metas []store.PageMeta) []apiRef {
	out := make([]apiRef, 0, len(metas))
	for _, m := range metas {
		out = append(out, apiRef{Slug: m.Slug, Title: m.Title})
	}
	return out
}

func (s *Server) apiPages(w http.ResponseWriter, r *http.Request) {
	metas, err := s.store.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	total := len(metas)
	limit := 1000
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v >= 0 {
		limit = v // 0 returns all pages from offset
	}
	offset := 0
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v > 0 {
		offset = v
	}
	if offset > total {
		offset = total
	}
	end := total
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	window := metas[offset:end]
	writeJSON(w, map[string]any{"pages": refs(window), "total": total, "offset": offset, "count": len(window)})
}

func (s *Server) apiPage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	page, err := s.store.Read(slug)
	if err != nil {
		if canon, ok := s.store.ResolveLink(slug); ok {
			page, err = s.store.Read(canon)
		}
	}
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"slug": page.Slug, "title": page.Title, "frontmatter": page.Frontmatter, "body": page.Body})
}

func (s *Server) apiSearch(w http.ResponseWriter, r *http.Request) {
	hits, err := s.search.Search(r.URL.Query().Get("q"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	truncated := len(hits) > store.SearchLimit
	if truncated {
		hits = hits[:store.SearchLimit]
	}
	writeJSON(w, map[string]any{"hits": hits, "truncated": truncated})
}

func (s *Server) apiGraph(w http.ResponseWriter, r *http.Request) {
	limit := 1000
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v >= 0 {
		limit = v // 0 returns the full graph
	}
	g, err := s.store.GraphViewTop(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, g)
}

func (s *Server) apiNeighbors(w http.ResponseWriter, r *http.Request) {
	depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
	if depth < 1 {
		depth = 1
	}
	if depth > 3 {
		depth = 3 // each hop widens toward the whole graph; bound it
	}
	nb, err := s.store.Neighbors(r.PathValue("slug"), depth)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(nb) > 200 { // Neighbors is nearest-first, so this keeps the closest
		nb = nb[:200]
	}
	writeJSON(w, map[string]any{"neighbors": nb})
}

func (s *Server) apiPath(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	metas, err := s.store.Path(q.Get("from"), q.Get("to"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"path": refs(metas)})
}

func (s *Server) apiHubs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 10
	}
	hubs, err := s.store.Hubs(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"hubs": hubs})
}

func (s *Server) apiHealth(w http.ResponseWriter, _ *http.Request) {
	h, err := s.store.Health()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, h)
}

func (s *Server) apiBacklinks(w http.ResponseWriter, r *http.Request) {
	metas, err := s.store.Backlinks(r.PathValue("slug"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(metas) > 500 {
		metas = metas[:500]
	}
	writeJSON(w, map[string]any{"pages": refs(metas)})
}

func (s *Server) apiTags(w http.ResponseWriter, _ *http.Request) {
	tags, err := s.store.Tags()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make(map[string][]apiRef, len(tags))
	for tag, metas := range tags {
		if len(metas) > 200 {
			metas = metas[:200]
		}
		out[tag] = refs(metas)
	}
	writeJSON(w, map[string]any{"tags": out})
}
