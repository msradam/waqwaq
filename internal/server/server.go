// Package server provides the web UI for browsing, searching, and reviewing
// wiki writes, plus the MCP endpoint mounted on the same mux and port.
package server

import (
	"bytes"
	"compress/gzip"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/crypto/bcrypt"

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
// When Users is set instead, the server runs a built-in login.
type WebPolicy struct {
	ProxyHeader string
	DefaultRole string
	Admins      []string
	Editors     []string
	Users       []WebUser
}

type WebUser struct {
	Name string
	Hash string // bcrypt
	Role string
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
	customCSS string    // path to .waqwaq/custom.css, or "" if absent
	base      string    // URL base path: "" single-wiki, "/w/<name>" in farm mode
	wikis     []WikiRef // all wikis, for the farm switcher (empty when single)
	secret    []byte    // per-run secret for signing session cookies
}

// WikiRef identifies a wiki for the farm switcher.
type WikiRef struct {
	Name    string
	Base    string
	Current bool
}

type Options struct {
	Store    *store.Store
	Renderer *render.Renderer
	MCP      *mcp.Server
	Auth     *auth.Registry
	Queue    *review.Queue
	Search   search.Searcher
	Rules    lint.Rules
	Web      WebPolicy
	ReadOnly bool
	Site     Site
	Base     string
	Wikis    []WikiRef
}

func New(o Options) (*Server, error) {
	tmpl, err := template.ParseFS(assets, "web/templates/*.html")
	if err != nil {
		return nil, err
	}
	title := o.Site.Title
	if title == "" {
		title = "Waqwaq"
	}
	theme := o.Site.Theme
	if theme == "" {
		theme = "auto"
	}
	custom := filepath.Join(o.Store.Root(), ".waqwaq", "custom.css")
	if _, err := os.Stat(custom); err != nil {
		custom = ""
	}
	searcher := o.Search
	if searcher == nil {
		searcher = o.Store
	}
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	return &Server{
		store: o.Store, renderer: o.Renderer, tmpl: tmpl, mcp: o.MCP, auth: o.Auth, queue: o.Queue,
		search: searcher, rules: o.Rules, web: o.Web, readOnly: o.ReadOnly,
		title: title, accent: o.Site.Accent, theme: theme, customCSS: custom,
		base: o.Base, wikis: o.Wikis, secret: secret,
	}, nil
}

func (s *Server) webEnabled() bool { return s.web.ProxyHeader != "" || len(s.web.Users) > 0 }

// role returns the caller's role. Open mode (no web auth) maps loopback or a
// trusted token to admin and everyone else to viewer. With a proxy header,
// identity is the header value; with built-in users, it is the signed session.
func (s *Server) role(r *http.Request) Role {
	switch {
	case s.web.ProxyHeader != "":
		user := strings.TrimSpace(r.Header.Get(s.web.ProxyHeader))
		if user == "" {
			return RoleNone
		}
		return s.proxyRole(user)
	case len(s.web.Users) > 0:
		c, err := r.Cookie("waqwaq_session")
		if err != nil {
			return RoleNone
		}
		name, ok := s.verifySession(c.Value)
		if !ok {
			return RoleNone
		}
		for _, u := range s.web.Users {
			if u.Name == name {
				return parseRole(u.Role)
			}
		}
		return RoleNone
	default:
		if s.canReview(r) {
			return RoleAdmin
		}
		return RoleViewer
	}
}

func (s *Server) proxyRole(user string) Role {
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

// webGuard requires an authenticated viewer for the human UI when web auth is
// configured. Health, MCP (its own token auth), static, and the login routes
// are exempt.
func (s *Server) webGuard(next http.Handler) http.Handler {
	if !s.webEnabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/healthz" || p == "/mcp" || strings.HasPrefix(p, "/mcp/") || strings.HasPrefix(p, "/static/") || p == "/login" || p == "/logout" {
			next.ServeHTTP(w, r)
			return
		}
		if s.role(r) == RoleNone {
			if len(s.web.Users) > 0 && r.Method == http.MethodGet {
				http.Redirect(w, r, s.base+"/login", http.StatusSeeOther)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) signSession(name string) string {
	msg := name + "|" + strconv.FormatInt(time.Now().Add(7*24*time.Hour).Unix(), 10)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(msg))
	return msg + "|" + hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) verifySession(value string) (string, bool) {
	parts := strings.Split(value, "|")
	if len(parts) != 3 {
		return "", false
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(parts[0] + "|" + parts[1]))
	if !hmac.Equal([]byte(parts[2]), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	return parts[0], true
}

type loginView struct {
	Chrome chrome
	Error  string
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		name, pass := r.FormValue("name"), r.FormValue("password")
		for _, u := range s.web.Users {
			if u.Name == name && bcrypt.CompareHashAndPassword([]byte(u.Hash), []byte(pass)) == nil {
				http.SetCookie(w, &http.Cookie{
					Name: "waqwaq_session", Value: s.signSession(name), Path: s.base + "/",
					HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600,
				})
				http.Redirect(w, r, s.base+"/", http.StatusSeeOther)
				return
			}
		}
		s.exec(w, "login.html", loginView{Chrome: s.chrome(r, "", ""), Error: "Invalid username or password."})
		return
	}
	s.exec(w, "login.html", loginView{Chrome: s.chrome(r, "", "")})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "waqwaq_session", Value: "", Path: s.base + "/", MaxAge: -1})
	http.Redirect(w, r, s.base+"/login", http.StatusSeeOther)
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
	s.registerAPI(mux)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/oracle", s.handleOracle)
	mux.HandleFunc("/graph.json", s.handleGraphJSON)
	mux.HandleFunc("/nav", s.handleNav)
	mux.HandleFunc("/tags/", s.handleTags)
	mux.HandleFunc("/tags", s.handleTags)
	mux.HandleFunc("/history/", s.handleHistory)
	mux.HandleFunc("/diff/", s.handleDiff)
	mux.HandleFunc("/edit/", s.handleEdit)
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/assets/", s.handleAsset)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/search", s.handleSearch)
	mux.HandleFunc("/wiki/", s.handlePage)
	mux.HandleFunc("/", s.handleIndex)
	return gzipWrap(s.webGuard(mux))
}

// gzipWrap compresses text responses (HTML pages, JSON) since the nav and graph
// payloads are large and highly repetitive. It skips /mcp (a streamed transport
// that must not be buffered) and the static and asset file servers (they set a
// Content-Length that gzip would invalidate, and their bytes are already small
// or pre-compressed).
func gzipWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") ||
			strings.HasPrefix(p, "/mcp") || strings.HasPrefix(p, "/static/") || strings.HasPrefix(p, "/assets/") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		w.Header().Del("Content-Length")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, gz: gz}, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	gz *gzip.Writer
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) { return g.gz.Write(b) }

func (g *gzipResponseWriter) Flush() {
	_ = g.gz.Flush()
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
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
	Base      string
	Wikis     []WikiRef
	Logout    bool
}

func (s *Server) chrome(r *http.Request, active, query string) chrome {
	pages, _ := s.store.List()
	pending, _ := s.queue.PendingCount()
	nav := buildNav(pages, active, s.base)
	if len(pages) > navInlineLimit {
		nav = trimNav(nav)
	}
	return chrome{
		Nav: nav, Pending: pending, Query: query, Active: active,
		ReadOnly: s.readOnly, CanEdit: !s.readOnly && s.role(r) >= RoleEditor,
		SiteTitle: s.title, Accent: s.accent, Theme: s.theme, CustomCSS: s.customCSS != "",
		Base: s.base, Wikis: s.wikis, Logout: len(s.web.Users) > 0,
	}
}

// navNode is a node in the sidebar tree: a folder (Children), a page (Slug), or
// both (a page that also has children under it).
type navNode struct {
	Name     string
	Path     string
	Slug     string
	Title    string
	Base     string
	Children []*navNode
	Open     bool
	Active   bool
	Lazy     bool // folder whose children load on expand (large wikis)
	More     int  // count of sibling leaves omitted at this level
}

// navInlineLimit is the page count below which the whole tree is rendered into
// the sidebar. Above it, the nav renders only the active spine and lazy-loads
// folder children on expand, so a page view does not ship every node.
const navInlineLimit = 1000

// navPerLevel caps how many leaves a single folder renders before collapsing the
// rest into a "+N more" hint, so a flat folder of 100k pages stays navigable.
const navPerLevel = 150

// trimNav prunes a fully built nav tree for a large wiki: folders off the active
// path become lazy (children dropped, loaded on demand), folders on it recurse,
// and surplus leaves at each level collapse into a More count.
func trimNav(nodes []*navNode) []*navNode {
	out := make([]*navNode, 0, len(nodes))
	shown, more := 0, 0
	for _, n := range nodes {
		if !n.Open && !n.Active && shown >= navPerLevel {
			more++
			continue
		}
		if len(n.Children) > 0 {
			if n.Open {
				n.Children = trimNav(n.Children)
			} else {
				n.Lazy = true
				n.Children = nil
			}
		}
		out = append(out, n)
		shown++
	}
	if more > 0 {
		out = append(out, &navNode{More: more})
	}
	return out
}

func findNav(nodes []*navNode, path string) *navNode {
	for _, n := range nodes {
		if n.Path == path {
			return n
		}
		if strings.HasPrefix(path, n.Path+"/") {
			if found := findNav(n.Children, path); found != nil {
				return found
			}
		}
	}
	return nil
}

func buildNav(metas []store.PageMeta, active, base string) []*navNode {
	root := &navNode{}
	byPath := make(map[string]*navNode, len(metas)) // O(1) child lookup; a linear scan is O(n^2) on a flat wiki
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
			child := byPath[path]
			if child == nil {
				child = &navNode{Name: part, Path: path, Base: base}
				cur.Children = append(cur.Children, child)
				byPath[path] = child
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

func markNav(nodes []*navNode, active string) {
	for _, n := range nodes {
		n.Active = n.Slug != "" && n.Slug == active
		n.Open = active == n.Path || strings.HasPrefix(active, n.Path+"/")
		markNav(n.Children, active)
	}
}

// handleUpload stores a multipart file as a content-addressed asset and returns
// its URL. Gated like editing.
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
	_ = json.NewEncoder(w).Encode(map[string]string{"url": s.base + "/assets/" + name})
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
	Stale    bool
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
		name := slug
		if i := strings.LastIndex(slug, "/"); i >= 0 {
			name = slug[i+1:]
		}
		if p, ok := s.store.VaultAsset(name); ok {
			http.ServeFile(w, r, p)
			return
		}
		if canon, ok := s.store.ResolveLink(slug); ok && canon != slug {
			http.Redirect(w, r, s.base+"/wiki/"+canon, http.StatusMovedPermanently)
			return
		}
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
		attr = &attributionView{
			Author: a.Author, Approver: a.Approver, When: relativeTime(a.When),
			Stale: !a.When.IsZero() && time.Since(a.When) > 90*24*time.Hour,
		}
	}
	backlinks, _ := s.store.Backlinks(page.Slug)
	s.exec(w, "page.html", pageView{
		Chrome: s.chrome(r, page.Slug, ""), Title: page.Title, Content: html, Slug: page.Slug,
		TOC: toc, Tags: store.FrontmatterTags(page.Frontmatter), Backlinks: backlinks, Attribution: attr,
	})
}

type editView struct {
	Chrome    chrome
	Slug      string
	Content   string
	Templates []string
	Issues    []lint.Issue
}

// handleEdit serves the in-browser editor. Saving runs the same lint as an MCP
// write before committing through the store, and requires editor access.
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
		http.Redirect(w, r, s.base+"/wiki/"+slug, http.StatusSeeOther)
		return
	}

	content := "---\ntitle: \n---\n\n"
	var templates []string
	if slug != "" {
		if p, err := s.store.Read(slug); err == nil {
			content = p.Raw
		}
	} else {
		templates = s.store.ListTemplates()
		if t := r.URL.Query().Get("template"); t != "" {
			if c, err := s.store.ReadTemplate(t); err == nil {
				content = c
			}
		}
	}
	s.exec(w, "edit.html", editView{Chrome: s.chrome(r, slug, ""), Slug: slug, Content: content, Templates: templates})
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

type oracleView struct {
	Chrome chrome
}

func (s *Server) handleOracle(w http.ResponseWriter, r *http.Request) {
	s.exec(w, "oracle.html", oracleView{Chrome: s.chrome(r, "oracle", "")})
}

// handleNav returns the rendered sidebar children of one folder, so a large
// wiki's nav can load a level at a time as the user expands it.
func (s *Server) handleNav(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	pages, _ := s.store.List()
	node := findNav(buildNav(pages, r.URL.Query().Get("active"), s.base), path)
	if node == nil {
		return
	}
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, "navtree", trimNav(node.Children)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) handleGraphJSON(w http.ResponseWriter, r *http.Request) {
	limit := 400
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v >= 0 {
		limit = v
	}
	g, err := s.store.GraphViewTop(limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(g)
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
	More   int
}

func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.Tags()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if tag := strings.TrimPrefix(r.URL.Path, "/tags/"); tag != "" && tag != r.URL.Path {
		pages, more := all[tag], 0
		if len(pages) > 500 {
			more, pages = len(pages)-500, pages[:500]
		}
		s.exec(w, "tags.html", tagsView{Chrome: s.chrome(r, "tags", ""), Tag: tag, Pages: pages, More: more})
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

// handleDiff diffs a revision against its parent.
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/diff/")
	rev := r.URL.Query().Get("rev")
	if slug == "" || rev == "" {
		http.NotFound(w, r)
		return
	}
	if !s.store.RevExists(rev) {
		http.Error(w, "unknown revision", http.StatusNotFound)
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
		http.Redirect(w, r, s.base+"/proposals", http.StatusSeeOther)
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
