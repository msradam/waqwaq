package core

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/adrg/frontmatter"
	"github.com/msradam/waqwaq/internal/search"
)

// defaultRefresh is how long a derived snapshot is trusted before the tree is
// re-stat'd. Within this window, reads return the cached view without walking,
// so a burst of requests on a large wiki costs one walk, not one per request.
// Changes on disk are reflected within this interval. Set to 0 to always
// re-stat (fully fresh, no staleness) via WithRefreshInterval.
const defaultRefresh = time.Second

const (
	wikiDirName = "wiki"
	rawDirName  = "raw"
	schemaFile  = "CLAUDE.md"
)

var wikilinkRe = regexp.MustCompile(`\[\[([^\]|]+)(?:\|[^\]]+)?\]\]`)

// mdLinkRe matches inline markdown links [text](target). Real OKF bundles link
// concepts with relative .md paths (e.g. [Orders](../tables/orders.md)) rather
// than [[wikilinks]], so both must resolve to graph edges.
var mdLinkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)

// reservedFiles are OKF's special files: index.md is the traversal entry point
// and log.md is the change log. Neither is a concept page, so both are exempt
// from the okf `type` requirement.
var reservedFiles = map[string]bool{"index": true, "log": true}

func isReserved(slug string) bool {
	return reservedFiles[path.Base(slug)]
}

// impl is the Core implementation. It is stateless in result — every call
// reflects the current filesystem — but memoizes derived work so it scales to
// large wikis.
//
// Two cache layers, both keyed off a cheap stat-only walk:
//   - files: parsed frontmatter + links per page, reused while a page's
//     (mtime,size) is unchanged, so unchanged pages are never re-read or
//     re-parsed.
//   - derived: the aggregated page list, link graph, degrees, and tag index,
//     rebuilt only when the corpus signature changes (a page added, removed, or
//     modified). All surfaces share it, so they cannot disagree.
//
// Every call still does one stat-only walk to confirm nothing changed; the
// expensive parse + aggregate happens once per change, not once per call.
type impl struct {
	root    string // wiki root (also the git root when versioned)
	pages   string // where .md pages live: root/wiki, or root itself
	raw     string // root/raw
	okfMode bool   // when true, a concept page missing OKF `type` is rejected on Read

	refresh time.Duration // how long a snapshot is trusted before re-stat'ing

	mu       sync.Mutex
	files    map[string]*fileEntry // per-page parse cache, keyed by slug
	sig      string                // corpus signature the derived data is built for
	derived  *derived
	walkedAt time.Time // when the tree was last stat-walked
}

// Option configures a Core.
type Option func(*impl)

// WithRefreshInterval sets how long a derived snapshot is trusted before the
// tree is re-stat'd. 0 means always re-stat (no staleness, slower on large
// wikis). The default is one second.
func WithRefreshInterval(d time.Duration) Option {
	return func(c *impl) { c.refresh = d }
}

// fileEntry is one page's cached parse, valid while (mtime,size) hold.
type fileEntry struct {
	mtime int64
	size  int64
	meta  PageMeta
	links []string // outbound link slugs
}

// derived is the aggregate view rebuilt when the corpus signature changes.
type derived struct {
	concepts []PageMeta          // reserved excluded, slug-sorted (the catalog)
	order    []string            // every slug incl. reserved, sorted
	metaBy   map[string]PageMeta // every page incl. reserved
	out      map[string][]string // slug -> outbound link slugs
	in       map[string][]string // slug -> inbound link slugs (reverse index), sorted
	adj      map[string]map[string]bool
	degree   map[string]int
	tags     []TagCount
}

// New creates a Core for a wiki directory. If the directory has a wiki/
// subdirectory, pages live there; otherwise the root itself holds pages, so a
// bare markdown folder or an Obsidian vault works unchanged.
func New(root string, okfMode bool, opts ...Option) (Core, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	pages := root
	if info, err := os.Stat(filepath.Join(root, wikiDirName)); err == nil && info.IsDir() {
		pages = filepath.Join(root, wikiDirName)
	}
	c := &impl{
		root:    root,
		pages:   pages,
		raw:     filepath.Join(root, rawDirName),
		okfMode: okfMode,
		refresh: defaultRefresh,
		files:   map[string]*fileEntry{},
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// Instructions returns the CLAUDE.md schema at the wiki root, or "" if absent.
func (c *impl) Instructions() string {
	data, err := os.ReadFile(filepath.Join(c.root, schemaFile))
	if err != nil {
		return ""
	}
	return string(data)
}

func (c *impl) slugFromPath(p string) (string, error) {
	rel, err := filepath.Rel(c.pages, p)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(strings.TrimSuffix(rel, ".md")), nil
}

func (c *impl) pathFromSlug(slug string) string {
	return filepath.Join(c.pages, filepath.FromSlash(slug)+".md")
}

func readFrontmatter(data []byte) (map[string]any, string) {
	var fm map[string]any
	rest, err := frontmatter.Parse(bytes.NewReader(data), &fm)
	if err != nil {
		return map[string]any{}, string(data)
	}
	if fm == nil {
		fm = map[string]any{}
	}
	return fm, string(rest)
}

// titleOr returns the frontmatter title, or a humanized fallback from the slug's
// basename when absent (real OKF concept files often omit `title`).
func titleOr(title, slug string) string {
	if title != "" {
		return title
	}
	base := path.Base(slug)
	base = strings.ReplaceAll(base, "-", " ")
	base = strings.ReplaceAll(base, "_", " ")
	return strings.TrimSpace(base)
}

func extractMetadata(fm map[string]any) PageMeta {
	var m PageMeta
	m.Title, _ = fm["title"].(string)
	m.Type, _ = fm["type"].(string)
	m.Description, _ = fm["description"].(string)
	m.Resource, _ = fm["resource"].(string)
	m.Timestamp, _ = fm["timestamp"].(string)
	m.Tags = extractTags(fm["tags"])
	return m
}

func extractTags(v any) []string {
	switch t := v.(type) {
	case []any:
		var tags []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				tags = append(tags, s)
			}
		}
		return tags
	case []string:
		return t
	case string:
		var tags []string
		for _, s := range strings.Split(t, ",") {
			if s = strings.TrimSpace(s); s != "" {
				tags = append(tags, s)
			}
		}
		return tags
	}
	return nil
}

// pageLinks returns the slugs a page links to, from both [[wikilinks]] and
// relative markdown .md links. fromSlug is the linking page's slug, used to
// resolve relative paths (../tables/orders.md) against its directory.
func pageLinks(fromSlug, body string) []string {
	var links []string
	seen := map[string]bool{}
	add := func(slug string) {
		if slug != "" && !seen[slug] {
			seen[slug] = true
			links = append(links, slug)
		}
	}

	for _, m := range wikilinkRe.FindAllStringSubmatch(body, -1) {
		add(strings.TrimSpace(m[1]))
	}

	dir := path.Dir(fromSlug)
	for _, m := range mdLinkRe.FindAllStringSubmatch(body, -1) {
		target := strings.TrimSpace(m[1])
		if slug, ok := relLinkToSlug(dir, target); ok {
			add(slug)
		}
	}
	return links
}

// relLinkToSlug converts a relative markdown link target to a slug, or reports
// false for external URLs, anchors, and non-markdown targets.
func relLinkToSlug(dir, target string) (string, bool) {
	if i := strings.IndexAny(target, "#?"); i >= 0 {
		target = target[:i] // drop anchor/query
	}
	if target == "" || !strings.HasSuffix(target, ".md") {
		return "", false
	}
	if strings.Contains(target, "://") || strings.HasPrefix(target, "//") || strings.HasPrefix(target, "mailto:") {
		return "", false
	}
	target = strings.TrimSuffix(target, ".md")
	joined := path.Join(dir, target) // resolves ./ and ../
	return strings.TrimPrefix(joined, "/"), true
}

// snapshot returns the current derived view, rebuilding only what changed since
// the last call. It does one stat-only walk to compute the corpus signature; if
// the signature matches, the cached derived is returned untouched.
func (c *impl) snapshot() (*derived, error) {
	// Fast path: within the refresh window, trust the last snapshot without
	// touching the filesystem at all.
	c.mu.Lock()
	if c.derived != nil && c.refresh > 0 && time.Since(c.walkedAt) < c.refresh {
		d := c.derived
		c.mu.Unlock()
		return d, nil
	}
	c.mu.Unlock()

	type ent struct {
		slug, path  string
		mtime, size int64
	}
	var ents []ent
	var sigSum, count uint64
	err := filepath.WalkDir(c.pages, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != c.pages && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".md") || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		slug, err := c.slugFromPath(p)
		if err != nil {
			return nil
		}
		e := ent{slug: slug, path: p, mtime: info.ModTime().UnixNano(), size: info.Size()}
		ents = append(ents, e)
		// Order-independent signature: a per-file hash summed with a count, so an
		// add, remove, or modify all change the signature without needing a sort.
		sigSum += fileHash(e.slug, e.mtime, e.size)
		count++
		return nil
	})
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%d:%d", sigSum, count)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.walkedAt = time.Now()
	if c.derived != nil && c.sig == key {
		return c.derived, nil // nothing changed: no re-parse, no re-aggregate
	}

	files := make(map[string]*fileEntry, len(ents))
	for _, e := range ents {
		if old, ok := c.files[e.slug]; ok && old.mtime == e.mtime && old.size == e.size {
			files[e.slug] = old // unchanged page: reuse the parse
			continue
		}
		data, err := os.ReadFile(e.path)
		if err != nil {
			continue
		}
		fm, body := readFrontmatter(data)
		meta := extractMetadata(fm)
		meta.Slug = e.slug
		meta.Title = titleOr(meta.Title, e.slug)
		files[e.slug] = &fileEntry{mtime: e.mtime, size: e.size, meta: meta, links: pageLinks(e.slug, body)}
	}
	c.files = files
	c.derived = buildDerived(files)
	c.sig = key
	return c.derived, nil
}

func fileHash(slug string, mtime, size int64) uint64 {
	const prime = 1099511628211
	var h uint64 = 14695981039346656037
	for i := 0; i < len(slug); i++ {
		h = (h ^ uint64(slug[i])) * prime
	}
	h = (h ^ uint64(mtime)) * prime
	h = (h ^ uint64(size)) * prime
	return h
}

// buildDerived assembles the aggregate view from the per-file cache: the concept
// catalog, the full node set, directed and undirected adjacency, degrees, and
// the tag index — each computed once per corpus change.
func buildDerived(files map[string]*fileEntry) *derived {
	d := &derived{
		metaBy: make(map[string]PageMeta, len(files)),
		out:    make(map[string][]string, len(files)),
		in:     map[string][]string{},
		adj:    map[string]map[string]bool{},
		degree: map[string]int{},
	}
	tagCounts := map[string]int{}
	for slug, fe := range files {
		d.metaBy[slug] = fe.meta
		d.out[slug] = fe.links
		d.order = append(d.order, slug)
		if !isReserved(slug) {
			d.concepts = append(d.concepts, fe.meta)
			for _, t := range fe.meta.Tags {
				tagCounts[t]++
			}
		}
	}
	sort.Strings(d.order)
	sort.Slice(d.concepts, func(i, j int) bool { return d.concepts[i].Slug < d.concepts[j].Slug })

	addEdge := func(a, b string) {
		if d.adj[a] == nil {
			d.adj[a] = map[string]bool{}
		}
		d.adj[a][b] = true
	}
	for from, tos := range d.out {
		for _, to := range tos {
			addEdge(from, to)
			addEdge(to, from)
			d.in[to] = append(d.in[to], from) // reverse index for O(degree) backlinks
		}
	}
	for slug, nbrs := range d.adj {
		d.degree[slug] = len(nbrs)
	}
	for slug := range d.in {
		sort.Strings(d.in[slug]) // deterministic backlink order
	}

	d.tags = make([]TagCount, 0, len(tagCounts))
	for t, n := range tagCounts {
		d.tags = append(d.tags, TagCount{Tag: t, Count: n})
	}
	sort.Slice(d.tags, func(i, j int) bool { return d.tags[i].Tag < d.tags[j].Tag })
	return d
}

// List returns concept pages filtered by prefix, type, and tag, with pagination.
func (c *impl) List(ctx context.Context, prefix, typeFilter, tagFilter string, limit, offset int) (ListResult, error) {
	d, err := c.snapshot()
	if err != nil {
		return ListResult{}, err
	}
	// d.concepts is already slug-sorted. With no filters, use it directly so a
	// paged list on a huge wiki is O(page), not O(pages).
	pages := d.concepts
	if prefix != "" || typeFilter != "" || tagFilter != "" {
		pages = make([]PageMeta, 0, len(d.concepts))
		for _, m := range d.concepts {
			if prefix != "" && !strings.HasPrefix(m.Slug, prefix) {
				continue
			}
			if typeFilter != "" && !strings.EqualFold(m.Type, typeFilter) {
				continue
			}
			if tagFilter != "" && !containsFold(m.Tags, tagFilter) {
				continue
			}
			pages = append(pages, m)
		}
	}

	if limit <= 0 {
		limit = 500
	}
	total := len(pages)
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return ListResult{Items: []PageMeta{}, Total: total, Truncated: false}, nil
	}
	end := offset + limit
	truncated := end < total
	if end > total {
		end = total
	}
	return ListResult{Items: pages[offset:end], Total: total, Truncated: truncated}, nil
}

// Read returns a page's full content with inlined outbound/inbound edges. In OKF
// mode a concept page missing `type` errors; the reserved files index.md and
// log.md are exempt (they carry no concept frontmatter).
func (c *impl) Read(ctx context.Context, slug string) (Page, error) {
	data, err := os.ReadFile(c.pathFromSlug(slug))
	if err != nil {
		return Page{}, fmt.Errorf("read page %q: %w", slug, err)
	}
	fm, body := readFrontmatter(data)
	if c.okfMode && !isReserved(slug) {
		if t, _ := fm["type"].(string); t == "" {
			return Page{}, fmt.Errorf("page %q missing required OKF `type` field", slug)
		}
	}
	title, _ := fm["title"].(string)

	page := Page{
		Slug:        slug,
		Title:       titleOr(title, slug),
		Frontmatter: fm,
		Body:        strings.TrimSpace(body),
	}

	// Inline edges from the shared graph so an agent navigates without a
	// read-per-neighbor.
	d, err := c.snapshot()
	if err != nil {
		return Page{}, err
	}
	ref := func(s string) EdgeRef {
		m := d.metaBy[s]
		return EdgeRef{Slug: s, Title: titleOr(m.Title, s), Type: m.Type}
	}
	for _, to := range d.out[slug] {
		page.Outbound = append(page.Outbound, ref(to))
	}
	for _, from := range d.in[slug] {
		page.Inbound = append(page.Inbound, ref(from))
	}
	return page, nil
}

// Search runs a keyword or regex query against page bodies, optionally narrowed
// by OKF type and tag. Ordering is deterministic by slug; Match carries type+tags
// so hits are decision-complete.
func (c *impl) Search(ctx context.Context, query string, isRegex bool, typeFilter, tagFilter string, limit int) ([]Match, error) {
	fetch := limit
	if typeFilter != "" || tagFilter != "" {
		fetch = 0 // over-fetch, then trim after filtering
	}
	hits, err := search.Search(c.pages, query, isRegex, fetch)
	if err != nil {
		return nil, err
	}
	// Search already scans every body; the metadata we need is only for the few
	// hits, so read those directly rather than snapshotting the whole tree.
	matches := make([]Match, 0, len(hits))
	for _, h := range hits {
		slug, err := c.slugFromPath(h.Path)
		if err != nil {
			continue
		}
		var meta PageMeta
		if data, err := os.ReadFile(h.Path); err == nil {
			fm, _ := readFrontmatter(data)
			meta = extractMetadata(fm)
		}
		if typeFilter != "" && !strings.EqualFold(meta.Type, typeFilter) {
			continue
		}
		if tagFilter != "" && !containsFold(meta.Tags, tagFilter) {
			continue
		}
		matches = append(matches, Match{
			Slug:    slug,
			Title:   titleOr(meta.Title, slug),
			Type:    meta.Type,
			Tags:    meta.Tags,
			Context: h.Context,
		})
		if limit > 0 && len(matches) >= limit {
			break
		}
	}
	return matches, nil
}

// Backlinks returns pages that link to the given slug.
func (c *impl) Backlinks(ctx context.Context, slug string) ([]string, error) {
	d, err := c.snapshot()
	if err != nil {
		return nil, err
	}
	// d.in[slug] is the pre-built, sorted reverse index: O(degree), not O(pages).
	backlinks := make([]string, len(d.in[slug]))
	copy(backlinks, d.in[slug])
	return backlinks, nil
}

// Neighbors returns a page's neighbors within `depth` hops (undirected), capped
// at depth 3 to bound expansion.
func (c *impl) Neighbors(ctx context.Context, slug string, depth int) ([]GraphNode, error) {
	if depth < 1 {
		depth = 1
	}
	if depth > 3 {
		depth = 3
	}
	d, err := c.snapshot()
	if err != nil {
		return nil, err
	}

	visited := map[string]bool{slug: true}
	frontier := []string{slug}
	found := map[string]bool{}
	for hop := 0; hop < depth && len(frontier) > 0; hop++ {
		var next []string
		for _, s := range frontier {
			for nbr := range d.adj[s] {
				if !visited[nbr] {
					visited[nbr] = true
					found[nbr] = true
					next = append(next, nbr)
				}
			}
		}
		frontier = next
	}

	var nodes []GraphNode
	for s := range found {
		m := d.metaBy[s] // zero value for dangling links; slug still useful
		nodes = append(nodes, GraphNode{Slug: s, Title: titleOr(m.Title, s), Type: m.Type, Degree: d.degree[s]})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Slug < nodes[j].Slug })
	return nodes, nil
}

// Tags returns tag counts, optionally filtered to a single tag.
func (c *impl) Tags(ctx context.Context, tag *string) ([]TagCount, error) {
	d, err := c.snapshot()
	if err != nil {
		return nil, err
	}
	if tag != nil {
		for _, tc := range d.tags {
			if strings.EqualFold(tc.Tag, *tag) {
				return []TagCount{tc}, nil
			}
		}
		return []TagCount{}, nil
	}
	out := make([]TagCount, len(d.tags))
	copy(out, d.tags)
	return out, nil
}

// Hubs returns the most-connected concept pages, sorted by degree descending.
// Reserved navigation files are excluded (consistent with the List catalog).
func (c *impl) Hubs(ctx context.Context, limit int) ([]GraphNode, error) {
	if limit <= 0 {
		limit = 10
	}
	d, err := c.snapshot()
	if err != nil {
		return nil, err
	}
	nodes := make([]GraphNode, 0, len(d.concepts))
	for _, m := range d.concepts {
		nodes = append(nodes, GraphNode{Slug: m.Slug, Title: m.Title, Type: m.Type, Degree: d.degree[m.Slug]})
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Degree != nodes[j].Degree {
			return nodes[i].Degree > nodes[j].Degree
		}
		return nodes[i].Slug < nodes[j].Slug
	})
	if len(nodes) > limit {
		nodes = nodes[:limit]
	}
	return nodes, nil
}

// Recent returns concept pages ordered by OKF `timestamp` (freshest first),
// pages without a timestamp last (then by slug). ISO 8601 timestamps sort
// correctly as strings, so no time parsing is needed.
func (c *impl) Recent(ctx context.Context, limit int) ([]PageMeta, error) {
	if limit <= 0 {
		limit = 20
	}
	d, err := c.snapshot()
	if err != nil {
		return nil, err
	}
	pages := make([]PageMeta, len(d.concepts))
	copy(pages, d.concepts)
	sort.SliceStable(pages, func(i, j int) bool {
		ti, tj := pages[i].Timestamp, pages[j].Timestamp
		if (ti == "") != (tj == "") {
			return ti != ""
		}
		if ti != tj {
			return ti > tj
		}
		return pages[i].Slug < pages[j].Slug
	})
	if len(pages) > limit {
		pages = pages[:limit]
	}
	return pages, nil
}

// Graph returns the full page link graph with undirected degree per node and
// deduped undirected edges. Reserved navigation files remain as nodes here,
// where graph structure matters.
func (c *impl) Graph(ctx context.Context) (Graph, error) {
	d, err := c.snapshot()
	if err != nil {
		return Graph{}, err
	}
	nodes := make([]GraphNode, 0, len(d.order))
	for _, slug := range d.order {
		m := d.metaBy[slug]
		nodes = append(nodes, GraphNode{Slug: slug, Title: titleOr(m.Title, slug), Type: m.Type, Degree: d.degree[slug]})
	}

	seen := map[[2]string]bool{}
	var edges []GraphEdge
	for from, tos := range d.out {
		for _, to := range tos {
			key := [2]string{from, to}
			if from > to {
				key = [2]string{to, from}
			}
			if !seen[key] {
				seen[key] = true
				edges = append(edges, GraphEdge{From: from, To: to})
			}
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	return Graph{Nodes: nodes, Edges: edges}, nil
}

// containsFold reports whether ss contains target, case-insensitively. OKF types
// and tags are producer-chosen strings, so tolerant matching is agent-friendly.
func containsFold(ss []string, target string) bool {
	for _, s := range ss {
		if strings.EqualFold(s, target) {
			return true
		}
	}
	return false
}
