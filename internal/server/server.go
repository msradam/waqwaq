// Package server is the read-only web UI: browse, read rendered markdown,
// search, tags, and a client-rendered link graph. Every handler resolves
// through the same core.Core the MCP and TUI surfaces use, so the surfaces
// cannot disagree. There is no write path, no auth, no theming config.
package server

import (
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"strings"

	"github.com/msradam/waqwaq/core"
	"github.com/msradam/waqwaq/internal/render"
)

// Site is the configurable appearance of the wiki: just its display name.
type Site struct {
	Title string
}

type Server struct {
	core     core.Core
	renderer *render.Renderer
	// layouts maps a layout name to a template set of base.html + that one
	// content file. Each content file defines "content", so they cannot share
	// a single set; one set per layout keeps them from colliding.
	layouts map[string]*template.Template
	static  http.Handler
	mcp     http.Handler
	site    Site
}

// New builds the web server over a read-only Core. mcpHandler is mounted at
// /mcp when non-nil.
func New(c core.Core, mcpHandler http.Handler, site Site) *Server {
	if site.Title == "" {
		site.Title = "waqwaq"
	}
	layouts := map[string]*template.Template{}
	for _, name := range []string{"page", "list", "search", "tags", "graph"} {
		layouts[name] = template.Must(template.ParseFS(assets,
			"web/templates/base.html",
			"web/templates/"+name+".html",
		))
	}
	sfs, _ := staticFS()
	return &Server{
		core:     c,
		renderer: render.New(""),
		layouts:  layouts,
		static:   http.FileServer(http.FS(sfsOr(sfs))),
		mcp:      mcpHandler,
		site:     site,
	}
}

func sfsOr(f fs.FS) fs.FS {
	if f == nil {
		return fs.FS(emptyFS{})
	}
	return f
}

type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

// Handler returns the fully wired mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/wiki/", s.handlePage)
	mux.HandleFunc("/list", s.handleList)
	mux.HandleFunc("/search", s.handleSearch)
	mux.HandleFunc("/tags", s.handleTags)
	mux.HandleFunc("/graph", s.handleGraph)
	mux.HandleFunc("/graph.json", s.handleGraphJSON)
	mux.Handle("/static/", http.StripPrefix("/static/", s.static))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	if s.mcp != nil {
		mux.Handle("/mcp", s.mcp)
		mux.Handle("/mcp/", s.mcp)
	}
	return mux
}

// viewData is the template payload. Layout selects the <main> class.
type viewData struct {
	Site       Site
	Title      string
	Layout     string
	Query      string
	Body       template.HTML
	TOC        []render.TOCEntry
	Page       core.Page
	Meta       *okfMeta
	Pages      []core.PageMeta
	Total      int
	Filter     string
	Hits       []core.Match
	TypeFilter string
	TagFilter  string
	Regex      bool
	Tags       []core.TagCount
}

// okfMeta is the frontmatter block shown in the page sidebar.
type okfMeta struct {
	Type        string
	Description string
	Resource    string
	Tags        []string
	Timestamp   string
}

func (s *Server) render(w http.ResponseWriter, data viewData) {
	data.Site = s.site
	t, ok := s.layouts[data.Layout]
	if !ok {
		http.Error(w, "unknown layout: "+data.Layout, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "base", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if _, err := s.core.Read(r.Context(), "index"); err == nil {
		http.Redirect(w, r, "/wiki/index", http.StatusFound)
		return
	}
	s.handleList(w, r)
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/wiki/")
	slug = strings.TrimSuffix(slug, "/")
	if slug == "" {
		http.Redirect(w, r, "/list", http.StatusFound)
		return
	}
	page, err := s.core.Read(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	body, toc, err := s.renderer.Render(page.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, viewData{
		Title:  page.Title,
		Layout: "page",
		Body:   body,
		TOC:    toc,
		Page:   page,
		Meta:   metaFrom(page),
	})
}

// metaFrom pulls the OKF frontmatter block for the sidebar, or nil if empty.
func metaFrom(p core.Page) *okfMeta {
	m := &okfMeta{}
	if v, ok := p.Frontmatter["type"].(string); ok {
		m.Type = v
	}
	if v, ok := p.Frontmatter["description"].(string); ok {
		m.Description = v
	}
	if v, ok := p.Frontmatter["resource"].(string); ok {
		m.Resource = v
	}
	if v, ok := p.Frontmatter["timestamp"].(string); ok {
		m.Timestamp = v
	}
	m.Tags = tagsFrom(p.Frontmatter["tags"])
	if m.Type == "" && m.Description == "" && m.Resource == "" && m.Timestamp == "" && len(m.Tags) == 0 {
		return nil
	}
	return m
}

func tagsFrom(v any) []string {
	switch t := v.(type) {
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		var out []string
		for _, s := range strings.Split(t, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	tag := r.URL.Query().Get("tag")
	res, err := s.core.List(r.Context(), "", "", tag, 0, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, viewData{
		Title:  "Pages",
		Layout: "list",
		Pages:  res.Items,
		Total:  res.Total,
		Filter: tag,
	})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := q.Get("q")
	typeFilter := q.Get("type")
	tagFilter := q.Get("tag")
	regex := q.Get("regex") != ""
	var hits []core.Match
	if query != "" || typeFilter != "" || tagFilter != "" {
		var err error
		hits, err = s.core.Search(r.Context(), query, regex, typeFilter, tagFilter, 100)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	s.render(w, viewData{
		Title:      "Search",
		Layout:     "search",
		Query:      query,
		Hits:       hits,
		TypeFilter: typeFilter,
		TagFilter:  tagFilter,
		Regex:      regex,
	})
}

func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.core.Tags(r.Context(), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, viewData{Title: "Tags", Layout: "tags", Tags: tags})
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	s.render(w, viewData{Title: "Graph", Layout: "graph"})
}

func (s *Server) handleGraphJSON(w http.ResponseWriter, r *http.Request) {
	g, err := s.core.Graph(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(g)
}
