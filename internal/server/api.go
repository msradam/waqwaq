package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/msradam/waqwaq/internal/store"
)

// The /api routes expose the same reads as the MCP tools as plain JSON, so a CI
// job, dashboard, or script that is not an MCP client can query the wiki. They
// follow the same access rules as the rest of the web UI.

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

func (s *Server) apiPages(w http.ResponseWriter, _ *http.Request) {
	metas, err := s.store.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"pages": refs(metas)})
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
	writeJSON(w, map[string]any{"hits": hits})
}

func (s *Server) apiGraph(w http.ResponseWriter, _ *http.Request) {
	g, err := s.store.GraphView()
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
	nb, err := s.store.Neighbors(r.PathValue("slug"), depth)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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
		out[tag] = refs(metas)
	}
	writeJSON(w, map[string]any{"tags": out})
}
