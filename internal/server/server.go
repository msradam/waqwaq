// Package server is the human-facing side: a web UI for browsing and searching
// the wiki and for reviewing proposed writes, plus the MCP endpoint mounted on
// the same mux and the same port.
package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/msradam/waqwaq/internal/auth"
	"github.com/msradam/waqwaq/internal/lint"
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

// WebPolicy configures web-UI access. When ProxyHeader is set, identity is read
// from that header (login and SSO are handled by the reverse proxy in front).
type WebPolicy struct {
	ProxyHeader string
	DefaultRole string
	Admins      []string
	Editors     []string
}

type Role int

const (
	RoleNone Role = iota
	RoleViewer
	RoleEditor
	RoleAdmin
)

func parseRole(s string) Role {
	switch s {
	case "admin":
		return RoleAdmin
	case "editor":
		return RoleEditor
	default:
		return RoleViewer
	}
}

type Server struct {
	store     *store.Store
	renderer  *render.Renderer
	tmpl      *template.Template
	mcp       *mcp.Server
	auth      *auth.Registry
	queue     *review.Queue
	search    search.Searcher
	rules     lint.Rules
	web       WebPolicy
	readOnly  bool
	title     string
	accent    string
	theme     string
	customCSS string // path to .waqwaq/custom.css, or "" if absent
}

func New(st *store.Store, rnd *render.Renderer, mcpSrv *mcp.Server, reg *auth.Registry, q *review.Queue, searcher search.Searcher, rules lint.Rules, web WebPolicy, readOnly bool, site Site) (*Server, error) {
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
		store: st, renderer: rnd, tmpl: tmpl, mcp: mcpSrv, auth: reg, queue: q, search: searcher, rules: rules, web: web, readOnly: readOnly,
		title: title, accent: site.Accent, theme: theme, customCSS: custom,
	}, nil
}

func (s *Server) webEnabled() bool { return s.web.ProxyHeader != "" }

// role returns the caller's role. In open mode (no proxy header) it falls back
// to the local/trusted check: loopback or a trusted token is admin, everyone
// else is a viewer. With a proxy header it maps the header identity to a role.
func (s *Server) role(r *http.Request) Role {
	if !s.webEnabled() {
		if s.canReview(r) {
			return RoleAdmin
		}
		return RoleViewer
	}
	user := strings.TrimSpace(r.Header.Get(s.web.ProxyHeader))
	if user == "" {
		return RoleNone
	}
	for _, a := range s.web.Admins {
		if a == user {
			return RoleAdmin
		}
	}
	for _, e := range s.web.Editors {
		if e == user {
			return RoleEditor
		}
	}
	if s.web.DefaultRole != "" {
		return parseRole(s.web.DefaultRole)
	}
	return RoleViewer
}

// webGuard requires an authenticated viewer for the human UI when a proxy header
// is configured. Health checks, MCP (its own token auth), and static assets are
// exempt.
func (s *Server) webGuard(next http.Handler) http.Handler {
	if !s.webEnabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/healthz" || p == "/mcp" || strings.HasPrefix(p, "/mcp/") || strings.HasPrefix(p, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		if s.role(r) == RoleNone {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
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
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/tags/", s.handleTags)
	mux.HandleFunc("/tags", s.handleTags)
	mux.HandleFunc("/history/", s.handleHistory)
	mux.HandleFunc("/diff/", s.handleDiff)
	mux.HandleFunc("/edit/", s.handleEdit)
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/assets/", s.handleAsset)
	mux.HandleFunc("/search", s.handleSearch)
	mux.HandleFunc("/wiki/", s.handlePage)
	mux.HandleFunc("/", s.handleIndex)
	return s.webGuard(mux)
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
	CanEdit   bool
	SiteTitle string
	Accent    string
	Theme     string
	CustomCSS bool
}

func (s *Server) chrome(r *http.Request, active, query string) chrome {
	pages, _ := s.store.List()
	pending, _ := s.queue.PendingCount()
	return chrome{
		Nav: buildNav(pages, active), Pending: pending, Query: query, Active: active,
		ReadOnly: s.readOnly, CanEdit: !s.readOnly && s.role(r) >= RoleEditor,
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

// handleUpload accepts a multipart file (images/pdf) from the editor, stores it
// as a content-addressed asset, and returns its URL. Gated like editing.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if s.readOnly || s.role(r) < RoleEditor {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "no file", http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	name, err := s.store.AddAsset(data, hdr.Filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"url": "/assets/" + name})
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	path, err := s.store.AssetPath(strings.TrimPrefix(r.URL.Path, "/assets/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, path)
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
	Tags        []string
	Backlinks   []store.PageMeta
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
			s.renderPage(w, r, page)
			return
		}
	}
	s.exec(w, "page.html", pageView{
		Chrome:  s.chrome(r, "", ""),
		Title:   "Waqwaq",
		Content: "<p>No index page yet. Pick a page from the sidebar, or create one with the <code>wiki_write</code> MCP tool.</p>",
	})
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/wiki/")
	page, err := s.store.Read(slug)
	if err != nil {
		s.notFound(w, r, slug)
		return
	}
	s.renderPage(w, r, page)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	hits, err := s.search.Search(q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.exec(w, "page.html", pageView{Chrome: s.chrome(r, "", q), Title: "Search", Query: q, IsSearch: true, Hits: hits})
}

func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, page *store.Page) {
	html, toc, err := s.renderer.Render(page.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var attr *attributionView
	if a, ok := s.store.LastTouched(page.Slug); ok {
		attr = &attributionView{Author: a.Author, Approver: a.Approver, When: relativeTime(a.When)}
	}
	backlinks, _ := s.store.Backlinks(page.Slug)
	s.exec(w, "page.html", pageView{
		Chrome: s.chrome(r, page.Slug, ""), Title: page.Title, Content: html, Slug: page.Slug,
		TOC: toc, Tags: store.FrontmatterTags(page.Frontmatter), Backlinks: backlinks, Attribution: attr,
	})
}

type editView struct {
	Chrome  chrome
	Slug    string
	Content string
	Issues  []lint.Issue
}

// handleEdit serves the in-browser editor. Saving runs the same lint as an MCP
// write and commits through the store. It is gated like the review actions:
// local access or a trusted token.
func (s *Server) handleEdit(w http.ResponseWriter, r *http.Request) {
	if s.readOnly {
		http.Error(w, "server is read-only", http.StatusForbidden)
		return
	}
	if s.role(r) < RoleEditor {
		http.Error(w, "editing requires editor access", http.StatusForbidden)
		return
	}
	slug := strings.Trim(strings.TrimPrefix(r.URL.Path, "/edit/"), "/")

	if r.Method == http.MethodPost {
		if slug == "" {
			slug = strings.Trim(strings.TrimSpace(r.FormValue("slug")), "/")
		}
		content := r.FormValue("content")
		if slug == "" {
			s.exec(w, "edit.html", editView{Chrome: s.chrome(r, "", ""), Content: content,
				Issues: []lint.Issue{{Severity: "error", Message: "a slug is required"}}})
			return
		}
		fm, body := store.SplitFrontmatter(content)
		known, _ := s.store.KnownSlugs()
		if issues := lint.Check(fm, body, known, s.rules); lint.HasErrors(issues) {
			s.exec(w, "edit.html", editView{Chrome: s.chrome(r, slug, ""), Slug: slug, Content: content, Issues: issues})
			return
		}
		author := fmt.Sprintf("%s <operator@waqwaq.local>", operatorName)
		if err := s.store.Write(slug, content, author, "waqwaq: edit "+slug); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/wiki/"+slug, http.StatusSeeOther)
		return
	}

	content := "---\ntitle: \n---\n\n"
	if slug != "" {
		if p, err := s.store.Read(slug); err == nil {
			content = p.Raw
		}
	}
	s.exec(w, "edit.html", editView{Chrome: s.chrome(r, slug, ""), Slug: slug, Content: content})
}

type healthView struct {
	Chrome chrome
	Health *store.Health
	Recent []store.Change
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	h, err := s.store.Health()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	recent, _ := s.store.Recent(15)
	s.exec(w, "health.html", healthView{Chrome: s.chrome(r, "health", ""), Health: h, Recent: recent})
}

type tagCount struct {
	Tag   string
	Count int
}

type tagsView struct {
	Chrome chrome
	Tag    string
	Tags   []tagCount
	Pages  []store.PageMeta
}

func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.Tags()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if tag := strings.TrimPrefix(r.URL.Path, "/tags/"); tag != "" && tag != r.URL.Path {
		s.exec(w, "tags.html", tagsView{Chrome: s.chrome(r, "tags", ""), Tag: tag, Pages: all[tag]})
		return
	}
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	counts := make([]tagCount, 0, len(names))
	for _, name := range names {
		counts = append(counts, tagCount{Tag: name, Count: len(all[name])})
	}
	s.exec(w, "tags.html", tagsView{Chrome: s.chrome(r, "tags", ""), Tags: counts})
}

type historyView struct {
	Chrome    chrome
	Slug      string
	Title     string
	Revisions []store.Revision
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/history/")
	page, err := s.store.Read(slug)
	if err != nil {
		s.notFound(w, r, slug)
		return
	}
	revs, _ := s.store.History(slug)
	s.exec(w, "history.html", historyView{Chrome: s.chrome(r, "", ""), Slug: slug, Title: page.Title, Revisions: revs})
}

type diffPageView struct {
	Chrome chrome
	Slug   string
	Rev    string
	Diff   []diffLine
}

// handleDiff shows what a single revision changed, the page at that commit
// compared against its parent.
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/diff/")
	rev := r.URL.Query().Get("rev")
	if slug == "" || rev == "" {
		http.NotFound(w, r)
		return
	}
	to, err := s.store.ReadAtRev(slug, rev)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	from, _ := s.store.ReadAtRev(slug, rev+"~1")
	s.exec(w, "diff.html", diffPageView{
		Chrome: s.chrome(r, "", ""), Slug: slug, Rev: rev,
		Diff: lineDiff(splitLines(from), splitLines(to)),
	})
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

func (s *Server) handleProposals(w http.ResponseWriter, r *http.Request) {
	ps, err := s.queue.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.exec(w, "proposals.html", proposalsView{Chrome: s.chrome(r, "proposals", ""), Proposals: ps})
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
		if s.role(r) < RoleAdmin {
			http.Error(w, "approving requires admin access", http.StatusForbidden)
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
		Chrome:  s.chrome(r, "proposals", ""),
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

func (s *Server) notFound(w http.ResponseWriter, r *http.Request, slug string) {
	msg := template.HTML("<h1>Page not found</h1><p class=\"muted\">There is no page at <code>" +
		template.HTMLEscapeString(slug) +
		"</code>. Pick a page from the sidebar, or create it with the <code>wiki_write</code> MCP tool.</p>")
	s.execStatus(w, http.StatusNotFound, "page.html", pageView{Chrome: s.chrome(r, "", ""), Title: "Not found", Content: msg})
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
