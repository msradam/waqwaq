// Package server is the human-facing side: a web UI for browsing and searching
// the wiki and for reviewing proposed writes, plus the MCP endpoint mounted on
// the same mux and the same port.
package server

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	fileServer := http.FileServer(http.FS(staticFS))
	mux.Handle("/static/", http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fileServer.ServeHTTP(w, r)
	})))

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

// canReview reports whether a request may approve or reject proposals. Local
// (loopback) requests always may, matching the trusted-operator model; remote
// requests need a trusted token, and only when tokens are configured. This keeps
// the privileged merge action from being open to the network when the server is
// bound to a public interface.
func (s *Server) canReview(r *http.Request) bool {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
			return true
		}
	}
	if !s.auth.Enabled() {
		return false
	}
	p, ok := s.auth.Resolve(r.Header.Get("Authorization"))
	return ok && p.Trusted
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
	Nav       []*navNode
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
		Nav: buildNav(pages, active), Pending: pending, Query: query, Active: active, ReadOnly: s.readOnly,
		SiteTitle: s.title, Accent: s.accent, Theme: s.theme, CustomCSS: s.customCSS != "",
	}
}

// navNode is a node in the sidebar tree: a folder (Children), a page (Slug), or
// both (a page that also has children under it).
type navNode struct {
	Name     string
	Path     string
	Slug     string
	Title    string
	Children []*navNode
	Open     bool
	Active   bool
}

func buildNav(metas []store.PageMeta, active string) []*navNode {
	root := &navNode{}
	for _, m := range metas {
		cur := root
		path := ""
		parts := strings.Split(m.Slug, "/")
		for i, part := range parts {
			if path == "" {
				path = part
			} else {
				path += "/" + part
			}
			child := childByName(cur, part)
			if child == nil {
				child = &navNode{Name: part, Path: path}
				cur.Children = append(cur.Children, child)
			}
			if i == len(parts)-1 {
				child.Slug = m.Slug
				child.Title = m.Title
			}
			cur = child
		}
	}
	markNav(root.Children, active)
	return root.Children
}

func childByName(parent *navNode, name string) *navNode {
	for _, c := range parent.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func markNav(nodes []*navNode, active string) {
	for _, n := range nodes {
		n.Active = n.Slug != "" && n.Slug == active
		n.Open = active == n.Path || strings.HasPrefix(active, n.Path+"/")
		markNav(n.Children, active)
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
	Chrome      chrome
	Title       string
	Content     template.HTML
	Slug        string
	TOC         []render.TOCEntry
	Attribution *attributionView
	IsSearch    bool
	Query       string
	Hits        []store.SearchHit
}

type attributionView struct {
	Author   string
	Approver string
	When     string
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
		s.notFound(w, slug)
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
	html, toc, err := s.renderer.Render(page.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var attr *attributionView
	if a, ok := s.store.LastTouched(page.Slug); ok {
		attr = &attributionView{Author: a.Author, Approver: a.Approver, When: relativeTime(a.When)}
	}
	s.exec(w, "page.html", pageView{Chrome: s.chrome(page.Slug, ""), Title: page.Title, Content: html, Slug: page.Slug, TOC: toc, Attribution: attr})
}

func relativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
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
		if !s.canReview(r) {
			http.Error(w, "review actions require local access or a trusted token", http.StatusForbidden)
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
	s.execStatus(w, http.StatusOK, name, data)
}

// execStatus renders to a buffer first, so a template error becomes a clean 500
// instead of a half-written page, and a non-200 status can be set before output.
func (s *Server) execStatus(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (s *Server) notFound(w http.ResponseWriter, slug string) {
	msg := template.HTML("<h1>Page not found</h1><p class=\"muted\">There is no page at <code>" +
		template.HTMLEscapeString(slug) +
		"</code>. Pick a page from the sidebar, or create it with the <code>wiki_write</code> MCP tool.</p>")
	s.execStatus(w, http.StatusNotFound, "page.html", pageView{Chrome: s.chrome("", ""), Title: "Not found", Content: msg})
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
