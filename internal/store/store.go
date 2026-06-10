// Package store is the git-backed markdown source of truth. Pages live under
// wiki/ (or the served folder itself when there is no wiki/, so a bare folder
// or Obsidian vault works unchanged), raw documents under raw/, schema in
// CLAUDE.md.
package store

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/gob"
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
	"unicode"

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

	sigMu    sync.Mutex // memoizes the corpus signature so cache checks need not re-stat the tree
	sigVal   string
	sigAt    time.Time
	sigStats map[string]fileStat // per-page stats from the same walk
	sigTTL   time.Duration       // adaptive: scales with the measured walk time

	listMu    sync.Mutex // guards the page-list cache below
	listSig   string
	listMetas []PageMeta

	infoMu     sync.Mutex // guards the per-page derived cache below
	infoMap    map[string]*pageInfo
	infoLoaded bool
	infoDirty  bool
	infoSaved  time.Time

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

	attrMu     sync.Mutex // guards the page-attribution cache below
	attrSig    string
	attrBySlug map[string]Attribution
	attrMisses map[string]*Attribution // per-slug fallback results, nil = no history

	resolvMu  sync.Mutex // guards the link-resolver cache below
	resolvSig string
	resolv    *linkResolver
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

// SearchLimit is how many hits search surfaces. Backends fetch one extra so a
// caller can tell the results were truncated and report it.
const SearchLimit = 50

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

// cleanSlug normalizes and validates a slug, returning its canonical form: no
// surrounding slashes and no trailing .md extension (so "notes.md" files under
// "notes", not "notes.md.md"). This is what the page is listed under, so a caller
// can echo it back.
func cleanSlug(slug string) (string, error) {
	raw := strings.TrimSuffix(strings.Trim(slug, "/"), ".md")
	var segs []string
	for _, seg := range strings.Split(raw, "/") {
		if seg == "" {
			continue // collapse repeated slashes, matching the filed path
		}
		if strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("invalid slug %q", slug)
		}
		if len(seg) > 200 { // stay under the filesystem's per-component limit with a clear error
			return "", fmt.Errorf("slug segment too long (max 200 characters)")
		}
		segs = append(segs, seg)
	}
	clean := strings.Join(segs, "/")
	if clean == "" {
		return "", errors.New("empty slug")
	}
	for _, r := range clean {
		// Reject control, zero-width, and bidi-control characters: invisible in a
		// slug, they enable spoofing and host-dependent collisions. Emoji and CJK
		// (not in these ranges) stay allowed.
		if unicode.IsControl(r) || isInvisibleFormat(r) {
			return "", fmt.Errorf("slug contains a disallowed character")
		}
	}
	return clean, nil
}

// CanonicalSlug returns the slug a page is actually filed under after
// normalization, or an error if the slug is invalid.
func (s *Store) CanonicalSlug(slug string) (string, error) { return cleanSlug(slug) }

func (s *Store) pathFor(slug string) (string, error) {
	clean, err := cleanSlug(slug)
	if err != nil {
		return "", err
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
	sig, stats, err := s.signatureStats()
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

	infos := s.pageInfos(stats)
	metas := make([]PageMeta, 0, len(infos))
	for slug, pi := range infos {
		metas = append(metas, PageMeta{Slug: slug, Title: pi.Title})
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Slug < metas[j].Slug })
	s.listMu.Lock()
	s.listSig, s.listMetas = sig, metas
	s.listMu.Unlock()
	return metas, nil
}

// fileStat is a page's cheap content fingerprint, computed from stats alone.
type fileStat struct {
	MtimeNano int64
	Size      int64
}

func (f fileStat) fp() string { return fmt.Sprintf("%d|%d", f.MtimeNano, f.Size) }

// sigTTLBounds clamp how long a computed Signature is reused before re-stating
// the tree. The TTL adapts to the measured walk time (50x, so the walk stays
// ~2% overhead): a small wiki re-checks every second, a 20k-page one every few
// seconds. Internal writes invalidate it immediately; the TTL only delays
// noticing edits made by another process, which a read-mostly wiki tolerates.
const (
	sigTTLMin = time.Second
	sigTTLMax = 15 * time.Second
)

// Signature is a hash of every page's path, mtime, and size, so a cache can
// detect staleness (including edits by external tools) from stats alone, no
// file reads. The result is memoized so the many cache checks that call it do
// not each re-walk a large tree.
func (s *Store) Signature() (string, error) {
	sig, _, err := s.signatureStats()
	return sig, err
}

// Fingerprints maps each page's slug to its content fingerprint. Incremental
// indexers diff it against their last view to re-read only the pages that
// changed. It shares the memoized walk with Signature.
func (s *Store) Fingerprints() (map[string]string, error) {
	_, stats, err := s.signatureStats()
	if err != nil {
		return nil, err
	}
	fps := make(map[string]string, len(stats))
	for slug, st := range stats {
		fps[slug] = st.fp()
	}
	return fps, nil
}

// signatureStats returns the corpus signature and every page's stats from a
// single memoized tree walk.
func (s *Store) signatureStats() (string, map[string]fileStat, error) {
	s.sigMu.Lock()
	if s.sigVal != "" && time.Since(s.sigAt) < s.sigTTL {
		v, st := s.sigVal, s.sigStats
		s.sigMu.Unlock()
		return v, st, nil
	}
	s.sigMu.Unlock()

	start := time.Now()
	sig, stats, err := s.walkStats()
	if err != nil {
		return "", nil, err
	}
	ttl := time.Since(start) * 50
	if ttl < sigTTLMin {
		ttl = sigTTLMin
	}
	if ttl > sigTTLMax {
		ttl = sigTTLMax
	}
	s.sigMu.Lock()
	s.sigVal, s.sigAt, s.sigStats, s.sigTTL = sig, time.Now(), stats, ttl
	s.sigMu.Unlock()
	return sig, stats, nil
}

func (s *Store) walkStats() (string, map[string]fileStat, error) {
	h := sha256.New()
	stats := make(map[string]fileStat)
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
		rel, _ := filepath.Rel(s.pages, path)
		slug := strings.TrimSuffix(filepath.ToSlash(rel), ".md")
		stats[slug] = fileStat{MtimeNano: info.ModTime().UnixNano(), Size: info.Size()}
		fmt.Fprintf(h, "%s|%d|%d\n", path, info.ModTime().UnixNano(), info.Size())
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	return hex.EncodeToString(h.Sum(nil)), stats, nil
}

// pageInfo is everything derived from one read of a page (title, tags, link
// extraction), cached against its fingerprint and persisted across processes,
// so the list, graph, and tag views share a single corpus read instead of
// each re-reading every page.
type pageInfo struct {
	Fp    string
	Title string
	Tags  []string
	Wiki  []wlink
	Md    []string
}

func derivePageInfo(raw, slug, fp string) *pageInfo {
	fm, body := SplitFrontmatter(raw)
	stripped := stripCode(body)
	return &pageInfo{
		Fp:    fp,
		Title: titleFrom(fm, body, slug),
		Tags:  FrontmatterTags(fm),
		Wiki:  wikiLinks(stripped),
		Md:    markdownLinkTargets(stripped),
	}
}

// pageInfos returns the derived info for every page in stats, re-reading only
// pages whose fingerprint changed. The cache is loaded from and saved to the
// wiki's cache directory, so a fresh process reuses what a previous one derived
// instead of re-reading the corpus.
func (s *Store) pageInfos(stats map[string]fileStat) map[string]*pageInfo {
	s.infoMu.Lock()
	if !s.infoLoaded {
		s.infoLoaded = true
		s.loadInfoCache()
	}
	if s.infoMap == nil {
		s.infoMap = make(map[string]*pageInfo, len(stats))
	}
	changed := false
	for slug, st := range stats {
		fp := st.fp()
		if pi, ok := s.infoMap[slug]; ok && pi.Fp == fp {
			continue
		}
		path := filepath.Join(s.pages, filepath.FromSlash(slug)+".md")
		data, err := os.ReadFile(path)
		if err != nil {
			delete(s.infoMap, slug)
			changed = true
			continue
		}
		s.infoMap[slug] = derivePageInfo(string(data), slug, fp)
		changed = true
	}
	for slug := range s.infoMap {
		if _, ok := stats[slug]; !ok {
			delete(s.infoMap, slug)
			changed = true
		}
	}
	snapshot := make(map[string]*pageInfo, len(s.infoMap))
	for k, v := range s.infoMap {
		snapshot[k] = v
	}
	var toSave map[string]*pageInfo
	if changed || s.infoDirty {
		// Rate-limit saves so a burst of writes does not re-serialize the cache
		// on every page; a dirty cache is retried on the next rebuild.
		if time.Since(s.infoSaved) > 5*time.Second {
			toSave, s.infoDirty, s.infoSaved = snapshot, false, time.Now()
		} else {
			s.infoDirty = true
		}
	}
	s.infoMu.Unlock()
	if toSave != nil {
		s.saveInfoCache(toSave)
	}
	return snapshot
}

// CacheDir returns this wiki's directory under the OS cache directory, keyed by
// the resolved root path so every name for the same physical wiki (notably /tmp
// vs /private/tmp on macOS) shares one cache.
func (s *Store) CacheDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	root := s.gitRoot
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	key := sha256.Sum256([]byte(root))
	dir := filepath.Join(cache, "waqwaq", hex.EncodeToString(key[:8]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// pages-v1 in the name lets a future cache layout change use a fresh file.
const infoCacheName = "pages-v1.gob"

type infoCacheData struct {
	Pages map[string]*pageInfo
}

func (s *Store) loadInfoCache() {
	dir, err := s.CacheDir()
	if err != nil {
		return
	}
	f, err := os.Open(filepath.Join(dir, infoCacheName))
	if err != nil {
		return
	}
	defer f.Close()
	var data infoCacheData
	if gob.NewDecoder(f).Decode(&data) != nil {
		return
	}
	s.infoMap = data.Pages
}

func (s *Store) saveInfoCache(pages map[string]*pageInfo) {
	dir, err := s.CacheDir()
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, "pages-*.tmp")
	if err != nil {
		return
	}
	if gob.NewEncoder(tmp).Encode(infoCacheData{Pages: pages}) != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return
	}
	if os.Rename(tmp.Name(), filepath.Join(dir, infoCacheName)) != nil {
		_ = os.Remove(tmp.Name())
	}
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
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("page %q not found: %w", slug, os.ErrNotExist) // avoid leaking the absolute path
		}
		return nil, err
	}
	raw := string(data)
	fm, body := SplitFrontmatter(raw)
	canon, _ := cleanSlug(slug) // pathFor already validated it
	return &Page{Slug: canon, Title: titleFrom(fm, body, canon), Frontmatter: fm, Body: body, Raw: raw}, nil
}

func (s *Store) Write(slug, content, author, message string) error {
	p, err := s.pathFor(slug)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, err := os.ReadFile(p); err == nil && string(existing) == content {
		return nil // identical content: a no-op, not an empty commit
	}
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
	s.sigVal, s.sigAt, s.sigStats = "", time.Time{}, nil
	s.sigMu.Unlock()
}

// Delete removes a page and commits the removal. The page stays recoverable from
// git history. It returns os.ErrNotExist when the slug has no page.
func (s *Store) Delete(slug, author, message string) error {
	p, err := s.pathFor(slug)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(p); err != nil {
		return err
	}
	rel, err := filepath.Rel(s.gitRoot, p)
	if err != nil {
		return err
	}
	// Snapshot an untracked working-tree file (e.g. an Obsidian-vault drop-in)
	// into git before removing it, so the deletion stays recoverable from
	// history. Without this, removing an untracked file and then trying to stage
	// it fails, leaving the file gone with nothing committed. commitDelete stays
	// false only when there is no git or the file cannot be staged (gitignored),
	// in which case it is a plain working-tree removal.
	commitDelete := false
	if s.git {
		commitDelete = s.tracked(rel) || s.commit("waqwaq: snapshot "+slug+" before delete", author, rel) == nil
	}
	if err := os.Remove(p); err != nil {
		return err
	}
	// Remove now-empty parent directories up to the pages root, so deleting the
	// last page in a folder does not leave an empty directory behind.
	for dir := filepath.Dir(p); dir != s.pages && strings.HasPrefix(dir, s.pages+sep); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			break // not empty
		}
	}
	if commitDelete {
		if err := s.commit(message, author, rel); err != nil {
			return err
		}
	}
	s.invalidateSignature()
	return nil
}

// isInvisibleFormat reports whether r is a zero-width or bidi-control character,
// which has no place in a slug.
func isInvisibleFormat(r rune) bool {
	switch {
	case r >= 0x200B && r <= 0x200F: // zero-width spaces/joiners, LRM/RLM
		return true
	case r >= 0x202A && r <= 0x202E: // bidi embeddings and overrides
		return true
	case r >= 0x2066 && r <= 0x2069: // bidi isolates
		return true
	case r == 0x2060 || r == 0xFEFF: // word joiner, zero-width no-break space/BOM
		return true
	}
	return false
}

// tracked reports whether a path (relative to the git root) is tracked by git.
func (s *Store) tracked(rel string) bool {
	cmd := exec.Command("git", "ls-files", "--error-unmatch", "--", rel)
	cmd.Dir = s.gitRoot
	return cmd.Run() == nil
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
			if len(hits) > SearchLimit { // one past, so the caller can detect truncation
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
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("raw document %q not found: %w", name, os.ErrNotExist) // avoid leaking the absolute path
	}
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

// DeleteRaw removes a raw source document. Raw documents are working-tree only
// (never committed), so this is a plain removal.
func (s *Store) DeleteRaw(name string) error {
	if err := safeRawName(name); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(s.raw, name)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("raw document %q not found: %w", name, os.ErrNotExist)
		}
		return err
	}
	return nil
}

func safeRawName(name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return fmt.Errorf("invalid raw document name %q", name)
	}
	return nil
}

func titleFrom(fm map[string]any, body, slug string) string {
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

// attrWindow is how many recent commits the batch attribution pass scans. A
// page last touched before that falls back to a one-off per-slug lookup.
const attrWindow = 5000

// LastTouched returns the author, the approver (parsed from the commit message),
// and time of the last commit to change a page; false when there is no history.
// Attributions come from one batched git log pass cached by Signature, so a page
// view does not fork a git subprocess.
func (s *Store) LastTouched(slug string) (*Attribution, bool) {
	if !s.git {
		return nil, false
	}
	canon, err := cleanSlug(slug)
	if err != nil {
		return nil, false
	}
	sig, err := s.Signature()
	if err != nil {
		return nil, false
	}
	s.attrMu.Lock()
	if sig != s.attrSig {
		s.attrBySlug = s.batchAttributions()
		s.attrMisses = make(map[string]*Attribution)
		s.attrSig = sig
	}
	if a, ok := s.attrBySlug[canon]; ok {
		s.attrMu.Unlock()
		return &a, true
	}
	if a, ok := s.attrMisses[canon]; ok {
		s.attrMu.Unlock()
		return a, a != nil
	}
	s.attrMu.Unlock()

	a := s.lastTouchedUncached(canon) // older than the batch window, or never committed
	s.attrMu.Lock()
	if sig == s.attrSig {
		s.attrMisses[canon] = a
	}
	s.attrMu.Unlock()
	return a, a != nil
}

// batchAttributions reads the newest commit touching each page from one
// streaming git log over the last attrWindow commits.
func (s *Store) batchAttributions() map[string]Attribution {
	pagesRel := filepath.ToSlash(s.relPages())
	cmd := exec.Command("git", "-c", "core.quotePath=false", "log", "-n", fmt.Sprint(attrWindow),
		"--name-status", "--format=%x01%an%x1f%aI%x1f%s")
	cmd.Dir = s.gitRoot
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return map[string]Attribution{}
	}
	if err := cmd.Start(); err != nil {
		return map[string]Attribution{}
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	out := make(map[string]Attribution)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var cur Attribution
	for sc.Scan() {
		line := sc.Text()
		if len(line) > 0 && line[0] == '\x01' {
			parts := strings.SplitN(line[1:], "\x1f", 3)
			cur = Attribution{Author: parts[0]}
			if len(parts) > 1 {
				cur.When, _ = time.Parse(time.RFC3339, parts[1])
			}
			if len(parts) > 2 {
				if m := approverRe.FindStringSubmatch(parts[2]); m != nil {
					cur.Approver = m[1]
				}
			}
			continue
		}
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		slug, ok := slugForGitPath(fields[len(fields)-1], pagesRel)
		if !ok {
			continue
		}
		if _, seen := out[slug]; !seen { // newest first: first occurrence wins
			out[slug] = cur
		}
	}
	return out
}

func (s *Store) lastTouchedUncached(slug string) *Attribution {
	p, err := s.pathFor(slug)
	if err != nil {
		return nil
	}
	rel, err := filepath.Rel(s.gitRoot, p)
	if err != nil {
		return nil
	}
	cmd := exec.Command("git", "log", "-1", "--format=%an%x1f%aI%x1f%s", "--", rel)
	cmd.Dir = s.gitRoot
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\x1f", 3)
	if len(parts) < 3 {
		return nil
	}
	when, _ := time.Parse(time.RFC3339, parts[1])
	a := &Attribution{Author: parts[0], When: when}
	if m := approverRe.FindStringSubmatch(parts[2]); m != nil {
		a.Approver = m[1]
	}
	return a
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
