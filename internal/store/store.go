// Package store is the git-backed markdown source of truth. Pages live under
// wiki/ (or the served folder itself when there is no wiki/, so a bare folder
// or Obsidian vault works unchanged), raw documents under raw/, schema in
// CLAUDE.md.
package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/adrg/frontmatter"
)

const (
	rawDirName  = "raw"
	wikiDirName = "wiki"
	schemaFile  = "CLAUDE.md"
)

var sep = string(os.PathSeparator)

var wikiLinkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

type Store struct {
	gitRoot string // versioned root (holds wiki/, raw/, CLAUDE.md)
	pages   string // where .md pages live: gitRoot/wiki or gitRoot
	raw     string // gitRoot/raw
	git     bool
	mu      sync.Mutex // serializes writes so concurrent commits cannot race on git

	sigMu  sync.Mutex // memoizes the corpus signature so cache checks need not re-stat the tree
	sigVal string
	sigAt  time.Time

	listMu    sync.Mutex // guards the page-list cache below
	listSig   string
	listMetas []PageMeta

	graphMu     sync.Mutex // guards the link-graph cache below
	graphSig    string
	graphMetas  []PageMeta
	graphEdges  []GraphEdge
	graphBroken []BrokenLink

	adjMu    sync.Mutex // guards the undirected-adjacency cache below
	adjSig   string
	adjMetas []PageMeta
	adjTitle map[string]string
	adjMap   map[string][]string

	tagsMu    sync.Mutex // guards the tag-index cache below
	tagsSig   string
	tagsCache map[string][]PageMeta

	assetMu  sync.Mutex // guards the asset-by-basename index below
	assetSig string
	assetMap map[string]string
}

type PageMeta struct {
	Slug  string
	Title string
}

type Page struct {
	Slug        string
	Title       string
	Frontmatter map[string]any
	Body        string // markdown with frontmatter stripped
	Raw         string // full file contents
}

type SearchHit struct {
	Slug    string `json:"slug"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func New(root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	s := &Store{gitRoot: abs, raw: filepath.Join(abs, rawDirName)}
	wiki := filepath.Join(abs, wikiDirName)
	if fi, err := os.Stat(wiki); err == nil && fi.IsDir() {
		s.pages = wiki
	} else {
		s.pages = abs
	}
	s.git = s.ensureGit()
	// Keep secrets and scratch out of history; settings (config.json, custom.css
	// under .waqwaq/) stay versioned.
	s.ensureIgnore(".waqwaq/tokens.json")
	s.ensureIgnore(".waqwaq/proposals/")
	return s, nil
}

// ensureIgnore appends pattern to the wiki's .gitignore if absent.
func (s *Store) ensureIgnore(pattern string) {
	if !s.git {
		return
	}
	path := filepath.Join(s.gitRoot, ".gitignore")
	data, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == pattern {
			return
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		_, _ = f.WriteString("\n")
	}
	_, _ = f.WriteString(pattern + "\n")
}

func (s *Store) Root() string  { return s.gitRoot }
func (s *Store) Pages() string { return s.pages }

// Layout reports whether pages are served from a wiki/ subdirectory or the
// folder itself (vault mode).
func (s *Store) Layout() string {
	if s.pages == s.gitRoot {
		return "folder"
	}
	return "wiki/"
}

func (s *Store) ensureGit() bool {
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(s.gitRoot, ".git")); err == nil {
		return true
	}
	cmd := exec.Command("git", "init")
	cmd.Dir = s.gitRoot
	return cmd.Run() == nil
}

func (s *Store) pathFor(slug string) (string, error) {
	clean := strings.Trim(slug, "/")
	if clean == "" {
		return "", errors.New("empty slug")
	}
	for _, seg := range strings.Split(clean, "/") {
		if strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("invalid slug %q", slug)
		}
	}
	p := filepath.Clean(filepath.Join(s.pages, filepath.FromSlash(clean)+".md"))
	if p != s.pages && !strings.HasPrefix(p, s.pages+sep) {
		return "", fmt.Errorf("invalid slug %q", slug)
	}
	if p == s.raw || strings.HasPrefix(p, s.raw+sep) {
		return "", fmt.Errorf("slug %q is inside the raw/ area", slug)
	}
	return p, nil
}

// Warm builds the corpus-wide caches (page list, link graph, adjacency, tags)
// ahead of the first request. Serve runs it in the background so an agent's
// first graph or tag query on a large wiki hits a warm cache instead of paying
// the full-corpus read. Errors are ignored: the caches rebuild lazily anyway.
func (s *Store) Warm() {
	_, _ = s.List()
	_, _, _, _ = s.adjacency()
	_, _ = s.Tags()
}

// List returns every page's slug and title, cached by Signature so the
// per-page title reads happen once per change rather than on every call.
func (s *Store) List() ([]PageMeta, error) {
	sig, err := s.Signature()
	if err != nil {
		return nil, err
	}
	s.listMu.Lock()
	if sig == s.listSig && s.listMetas != nil {
		metas := s.listMetas
		s.listMu.Unlock()
		return metas, nil
	}
	s.listMu.Unlock()

	metas, err := s.listUncached()
	if err != nil {
		return nil, err
	}
	s.listMu.Lock()
	s.listSig, s.listMetas = sig, metas
	s.listMu.Unlock()
	return metas, nil
}

func (s *Store) listUncached() ([]PageMeta, error) {
	var metas []PageMeta
	err := filepath.WalkDir(s.pages, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != s.pages && (d.Name() == ".git" || path == s.raw) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		rel, _ := filepath.Rel(s.pages, path)
		slug := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		metas = append(metas, PageMeta{Slug: slug, Title: s.titleOf(path, slug)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Slug < metas[j].Slug })
	return metas, nil
}

// sigTTL bounds how long a computed Signature is reused before re-stating the
// tree. Internal writes invalidate it immediately; this only delays noticing
// edits made by another process, which a read-mostly wiki tolerates.
const sigTTL = time.Second

// Signature is a hash of every page's path, mtime, and size, so a cache can
// detect staleness (including edits by external tools) from stats alone, no
// file reads. The result is memoized for sigTTL so the many cache checks that
// call it do not each re-walk a large tree.
func (s *Store) Signature() (string, error) {
	s.sigMu.Lock()
	if s.sigVal != "" && time.Since(s.sigAt) < sigTTL {
		v := s.sigVal
		s.sigMu.Unlock()
		return v, nil
	}
	s.sigMu.Unlock()

	sig, err := s.computeSignature()
	if err != nil {
		return "", err
	}
	s.sigMu.Lock()
	s.sigVal, s.sigAt = sig, time.Now()
	s.sigMu.Unlock()
	return sig, nil
}

func (s *Store) computeSignature() (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(s.pages, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != s.pages && (d.Name() == ".git" || path == s.raw) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s|%d|%d\n", path, info.ModTime().UnixNano(), info.Size())
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Store) KnownSlugs() (map[string]bool, error) {
	metas, err := s.List()
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(metas))
	for _, m := range metas {
		known[m.Slug] = true
	}
	return known, nil
}

func (s *Store) Read(slug string) (*Page, error) {
	p, err := s.pathFor(slug)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	raw := string(data)
	fm, body := SplitFrontmatter(raw)
	return &Page{Slug: slug, Title: s.titleOf(p, slug), Frontmatter: fm, Body: body, Raw: raw}, nil
}

func (s *Store) Write(slug, content, author, message string) error {
	p, err := s.pathFor(slug)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return err
	}
	rel, err := filepath.Rel(s.gitRoot, p)
	if err != nil {
		return err
	}
	paths := []string{rel}
	if _, err := os.Stat(filepath.Join(s.gitRoot, ".gitignore")); err == nil {
		paths = append(paths, ".gitignore")
	}
	if err := s.commit(message, author, paths...); err != nil {
		return err
	}
	s.invalidateSignature()
	return nil
}

// invalidateSignature forces the next Signature call to re-stat the tree, so a
// just-written page is reflected without waiting for the memo to expire.
func (s *Store) invalidateSignature() {
	s.sigMu.Lock()
	s.sigVal, s.sigAt = "", time.Time{}
	s.sigMu.Unlock()
}

func (s *Store) Search(query string) ([]SearchHit, error) {
	metas, err := s.List()
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return nil, nil
	}
	var hits []SearchHit
	for _, m := range metas {
		page, err := s.Read(m.Slug)
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(page.Title+"\n"+page.Body), needle) {
			hits = append(hits, SearchHit{Slug: m.Slug, Title: m.Title, Snippet: snippet(page.Body, needle)})
			if len(hits) >= 50 {
				break
			}
		}
	}
	return hits, nil
}

// Graph returns the pages and the resolved [[wikilink]] edges between them.
func (s *Store) Graph() ([]PageMeta, []GraphEdge, error) {
	metas, edges, _, err := s.graphData()
	return metas, edges, err
}

// Instructions returns the contents of CLAUDE.md at the wiki root, if present.
func (s *Store) Instructions() string {
	data, err := os.ReadFile(filepath.Join(s.gitRoot, schemaFile))
	if err != nil {
		return ""
	}
	return string(data)
}

func (s *Store) ListRaw() ([]string, error) {
	entries, err := os.ReadDir(s.raw)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *Store) ReadRaw(name string) (string, error) {
	if err := safeRawName(name); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(s.raw, name))
	return string(data), err
}

func (s *Store) AddRaw(name string, data []byte) error {
	if err := safeRawName(name); err != nil {
		return err
	}
	if err := os.MkdirAll(s.raw, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.raw, name), data, 0o644)
}

func safeRawName(name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf("invalid raw document name %q", name)
	}
	return nil
}

func (s *Store) titleOf(path, slug string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return filepath.Base(slug)
	}
	fm, body := SplitFrontmatter(string(data))
	if t, ok := fm["title"].(string); ok && strings.TrimSpace(t) != "" {
		return t
	}
	for _, line := range strings.Split(body, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "# ") {
			return strings.TrimSpace(t[2:])
		}
	}
	return filepath.Base(slug)
}

// commit stages only the given paths (relative to gitRoot), so a write never
// sweeps in unrelated working-tree changes.
func (s *Store) commit(message, author string, paths ...string) error {
	if !s.git || len(paths) == 0 {
		return nil
	}
	add := exec.Command("git", append([]string{"add", "--"}, paths...)...)
	add.Dir = s.gitRoot
	if err := add.Run(); err != nil {
		return err
	}
	args := []string{"commit", "-m", message, "--allow-empty"}
	if author != "" {
		args = append(args, "--author", author)
	}
	c := exec.Command("git", args...)
	c.Dir = s.gitRoot
	c.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=waqwaq", "GIT_AUTHOR_EMAIL=waqwaq@local",
		"GIT_COMMITTER_NAME=waqwaq", "GIT_COMMITTER_EMAIL=waqwaq@local",
	)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("git commit: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

type Attribution struct {
	Author   string
	Approver string
	When     time.Time
}

var approverRe = regexp.MustCompile(`approved by (\S+)`)

// LastTouched returns the author, the approver (parsed from the commit message),
// and time of the last commit to change a page; false when there is no history.
func (s *Store) LastTouched(slug string) (*Attribution, bool) {
	if !s.git {
		return nil, false
	}
	p, err := s.pathFor(slug)
	if err != nil {
		return nil, false
	}
	rel, err := filepath.Rel(s.gitRoot, p)
	if err != nil {
		return nil, false
	}
	cmd := exec.Command("git", "log", "-1", "--format=%an%x1f%aI%x1f%s", "--", rel)
	cmd.Dir = s.gitRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\x1f", 3)
	if len(parts) < 3 {
		return nil, false
	}
	when, _ := time.Parse(time.RFC3339, parts[1])
	a := &Attribution{Author: parts[0], When: when}
	if m := approverRe.FindStringSubmatch(parts[2]); m != nil {
		a.Approver = m[1]
	}
	return a, true
}

// SplitFrontmatter separates a leading frontmatter block from the markdown body.
// It reads YAML (--- ... ---), TOML (+++ ... +++), and JSON, and returns a nil
// map when there is no frontmatter.
func SplitFrontmatter(raw string) (map[string]any, string) {
	norm := strings.ReplaceAll(raw, "\r\n", "\n")
	var fm map[string]any
	rest, err := frontmatter.Parse(strings.NewReader(norm), &fm)
	if err != nil || len(fm) == 0 {
		return nil, raw
	}
	return fm, strings.TrimPrefix(string(rest), "\n")
}

func snippet(body, needle string) string {
	flat := strings.Join(strings.Fields(body), " ")
	i := strings.Index(strings.ToLower(flat), needle)
	if i < 0 {
		if len(flat) > 160 {
			return flat[:160] + "…"
		}
		return flat
	}
	start := i - 60
	if start < 0 {
		start = 0
	}
	end := i + 100
	if end > len(flat) {
		end = len(flat)
	}
	out := flat[start:end]
	if start > 0 {
		out = "…" + out
	}
	if end < len(flat) {
		out += "…"
	}
	return out
}
