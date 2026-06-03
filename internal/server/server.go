// Package server is the human-facing side: a web UI for browsing and searching
// the wiki, plus the MCP endpoint mounted on the same mux and the same port.
package server

import (
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/msradam/waqwaq/internal/render"
	"github.com/msradam/waqwaq/internal/store"
)

type Server struct {
	store    *store.Store
	renderer *render.Renderer
	tmpl     *template.Template
	mcp      *mcp.Server
}

func New(st *store.Store, rnd *render.Renderer, mcpSrv *mcp.Server) (*Server, error) {
	tmpl, err := template.ParseFS(assets, "web/templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Server{store: st, renderer: rnd, tmpl: tmpl, mcp: mcpSrv}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	staticFS, _ := fs.Sub(assets, "web/static")
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, nil)
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/mcp/", mcpHandler)

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/search", s.handleSearch)
	mux.HandleFunc("/wiki/", s.handlePage)
	mux.HandleFunc("/", s.handleIndex)
	return mux
}

type viewData struct {
	Title    string
	Content  template.HTML
	Slug     string
	Pages    []store.PageMeta
	Query    string
	Hits     []store.SearchHit
	IsSearch bool
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	for _, slug := range []string{"index", "README", "readme", "Home", "home"} {
		if page, err := s.store.Read(slug); err == nil {
			s.renderPage(w, page)
			return
		}
	}
	s.render(w, viewData{Title: "Waqwaq", Content: "<p>No index page yet. Pick a page from the sidebar, or create one via the <code>write_page</code> MCP tool.</p>"})
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/wiki/")
	page, err := s.store.Read(slug)
	if err != nil {
		http.Error(w, "page not found: "+slug, http.StatusNotFound)
		return
	}
	s.renderPage(w, page)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	hits, err := s.store.Search(q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, viewData{Title: "Search", Query: q, Hits: hits, IsSearch: true})
}

func (s *Server) renderPage(w http.ResponseWriter, page *store.Page) {
	html, err := s.renderer.Render(page.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, viewData{Title: page.Title, Content: html, Slug: page.Slug})
}

func (s *Server) render(w http.ResponseWriter, data viewData) {
	pages, err := s.store.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data.Pages = pages
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "page.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
