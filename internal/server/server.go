// Package server is the human-facing side: a web UI for browsing and searching
// the wiki and for reviewing proposed writes, plus the MCP endpoint mounted on
// the same mux and the same port.
package server

import (
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/msradam/waqwaq/internal/auth"
	"github.com/msradam/waqwaq/internal/render"
	"github.com/msradam/waqwaq/internal/review"
	"github.com/msradam/waqwaq/internal/search"
	"github.com/msradam/waqwaq/internal/store"
)

const operatorName = "operator"

// Site holds the configurable appearance of the wiki.
type Site struct {
	Title  string
	Accent string
	Theme  string
}

type Server struct {
	store     *store.Store
	renderer  *render.Renderer
	tmpl      *template.Template
	mcp       *mcp.Server
	auth      *auth.Registry
	queue     *review.Queue
	search    search.Searcher
	readOnly  bool
	title     string
	accent    string
	theme     string
	customCSS string // path to .waqwaq/custom.css, or "" if absent
}

func New(st *store.Store, rnd *render.Renderer, mcpSrv *mcp.Server, reg *auth.Registry, q *review.Queue, searcher search.Searcher, readOnly bool, site Site) (*Server, error) {
	tmpl, err := template.ParseFS(assets, "web/templates/*.html")
	if err != nil {
		return nil, err
	}
	title := site.Title
	if title == "" {
		title = "Waqwaq"
	}
	theme := site.Theme
	if theme == "" {
		theme = "auto"
	}
	custom := filepath.Join(st.Root(), ".waqwaq", "custom.css")
	if _, err := os.Stat(custom); err != nil {
		custom = ""
	}
	if searcher == nil {
		searcher = st
	}
	return &Server{
		store: st, renderer: rnd, tmpl: tmpl, mcp: mcpSrv, auth: reg, queue: q, search: searcher, readOnly: readOnly,
		title: title, accent: site.Accent, theme: theme, customCSS: custom,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	staticFS, _ := fs.Sub(assets, "web/static")
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	mcpHandler := s.authWrap(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, nil))
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/mcp/", mcpHandler)

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/custom.css", s.handleCustomCSS)
	mux.HandleFunc("/proposals/", s.handleProposal)
	mux.HandleFunc("/proposals", s.handleProposals)
	mux.HandleFunc("/search", s.handleSearch)
	mux.HandleFunc("/wiki/", s.handlePage)
	mux.HandleFunc("/", s.handleIndex)
	return mux
}

// authWrap rejects MCP requests that lack a valid bearer token, but only when
// tokens are configured. In open mode it passes everything through.
func (s *Server) authWrap(next http.Handler) http.Handler {
	if !s.auth.Enabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.auth.Resolve(r.Header.Get("Authorization")); !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type chrome struct {
	Pages     []store.PageMeta
	Pending   int
	Query     string
	Active    string
	ReadOnly  bool
	SiteTitle string
	Accent    string
	Theme     string
	CustomCSS bool
}

func (s *Server) chrome(active, query string) chrome {
	pages, _ := s.store.List()
	pending, _ := s.queue.PendingCount()
	return chrome{
		Pages: pages, Pending: pending, Query: query, Active: active, ReadOnly: s.readOnly,
		SiteTitle: s.title, Accent: s.accent, Theme: s.theme, CustomCSS: s.customCSS != "",
	}
}

func (s *Server) handleCustomCSS(w http.ResponseWriter, r *http.Request) {
	if s.customCSS == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	http.ServeFile(w, r, s.customCSS)
}

type pageView struct {
	Chrome   chrome
	Title    string
	Content  template.HTML
	Slug     string
	IsSearch bool
	Query    string
	Hits     []store.SearchHit
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
	s.exec(w, "page.html", pageView{
		Chrome:  s.chrome("", ""),
		Title:   "Waqwaq",
		Content: "<p>No index page yet. Pick a page from the sidebar, or create one with the <code>wiki_write</code> MCP tool.</p>",
	})
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
	hits, err := s.search.Search(q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.exec(w, "page.html", pageView{Chrome: s.chrome("", q), Title: "Search", Query: q, IsSearch: true, Hits: hits})
}

func (s *Server) renderPage(w http.ResponseWriter, page *store.Page) {
	html, err := s.renderer.Render(page.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.exec(w, "page.html", pageView{Chrome: s.chrome(page.Slug, ""), Title: page.Title, Content: html, Slug: page.Slug})
}

type proposalsView struct {
	Chrome    chrome
	Proposals []*review.Proposal
}

func (s *Server) handleProposals(w http.ResponseWriter, _ *http.Request) {
	ps, err := s.queue.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.exec(w, "proposals.html", proposalsView{Chrome: s.chrome("proposals", ""), Proposals: ps})
}

type proposalView struct {
	Chrome  chrome
	P       *review.Proposal
	Stale   bool
	Pending bool
	Diff    []diffLine
}

func (s *Server) handleProposal(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/proposals/")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	if r.Method == http.MethodPost {
		if s.readOnly {
			http.Error(w, "server is read-only", http.StatusForbidden)
			return
		}
		var err error
		switch r.FormValue("action") {
		case "approve":
			_, err = s.queue.Merge(id, operatorName)
		case "reject":
			_, err = s.queue.Reject(id, operatorName, strings.TrimSpace(r.FormValue("reason")))
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/proposals", http.StatusSeeOther)
		return
	}

	p, err := s.queue.Get(id)
	if err != nil {
		http.Error(w, "proposal not found: "+id, http.StatusNotFound)
		return
	}
	var base string
	if cur, err := s.store.Read(p.Slug); err == nil {
		base = cur.Raw
	}
	s.exec(w, "proposal.html", proposalView{
		Chrome:  s.chrome("proposals", ""),
		P:       p,
		Stale:   s.queue.Stale(p),
		Pending: p.Status == review.Pending,
		Diff:    lineDiff(splitLines(base), splitLines(p.Content)),
	})
}

func (s *Server) exec(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

type diffLine struct {
	Op   string // "ctx", "add", "del"
	Text string
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

// lineDiff produces a line-level diff of a into b using a longest-common-
// subsequence walk. Memory is O(len(a)*len(b)), which is fine for wiki pages.
func lineDiff(a, b []string) []diffLine {
	n, m := len(a), len(b)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []diffLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, diffLine{"ctx", a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, diffLine{"del", a[i]})
			i++
		default:
			out = append(out, diffLine{"add", b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, diffLine{"del", a[i]})
	}
	for ; j < m; j++ {
		out = append(out, diffLine{"add", b[j]})
	}
	return out
}
