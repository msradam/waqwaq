package store

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// staleDays is how long a page can go untouched before the health view flags it.
const staleDays = 90

type BrokenLink struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type StalePage struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
	Days  int    `json:"days"`
}

type Health struct {
	Orphans []PageMeta   `json:"orphans"`
	Broken  []BrokenLink `json:"broken"`
	Stale   []StalePage  `json:"stale"`
}

// graphData builds the link graph (pages, resolved [[wikilink]] edges, broken
// links to missing pages), cached by signature so it recomputes only on change.
func (s *Store) graphData() ([]PageMeta, []GraphEdge, []BrokenLink, error) {
	sig, err := s.Signature()
	if err != nil {
		return nil, nil, nil, err
	}
	s.graphMu.Lock()
	defer s.graphMu.Unlock()
	if sig == s.graphSig && s.graphMetas != nil {
		return s.graphMetas, s.graphEdges, s.graphBroken, nil
	}
	metas, err := s.List()
	if err != nil {
		return nil, nil, nil, err
	}
	resolver := newLinkResolver(metas)
	var edges []GraphEdge
	var broken []BrokenLink
	for _, m := range metas {
		page, err := s.Read(m.Slug)
		if err != nil {
			continue
		}
		body := stripCode(page.Body)
		seenTo := map[string]bool{}
		addEdge := func(canon string) {
			if canon != "" && canon != m.Slug && !seenTo[canon] {
				seenTo[canon] = true
				edges = append(edges, GraphEdge{From: m.Slug, To: canon})
			}
		}
		for _, lk := range wikiLinks(body) {
			if lk.embed {
				continue // image/note transclusions are content, not page edges
			}
			canon := resolveTarget(resolver, lk.target)
			if canon == "" {
				// Skip same-page anchors ([[#heading]]), assets, and external URLs.
				if name := brokenName(lk.target); name != "" && !isAssetRef(lk.target) && !strings.Contains(lk.target, "://") {
					broken = append(broken, BrokenLink{From: m.Slug, To: name})
				}
				continue
			}
			addEdge(canon)
		}
		// Plain markdown links to internal pages also count as edges, but a missing
		// one is not flagged broken: markdown links to external files are benign.
		for _, t := range markdownLinkTargets(body) {
			addEdge(resolver.resolve(t))
		}
	}
	s.graphSig, s.graphMetas, s.graphEdges, s.graphBroken = sig, metas, edges, broken
	return metas, edges, broken, nil
}

// Backlinks returns the pages that link to the given slug.
func (s *Store) Backlinks(slug string) ([]PageMeta, error) {
	metas, edges, _, err := s.graphData()
	if err != nil {
		return nil, err
	}
	title := make(map[string]string, len(metas))
	for _, m := range metas {
		title[m.Slug] = m.Title
	}
	seen := map[string]bool{}
	var in []PageMeta
	for _, e := range edges {
		if e.To == slug && !seen[e.From] {
			seen[e.From] = true
			in = append(in, PageMeta{Slug: e.From, Title: title[e.From]})
		}
	}
	sort.Slice(in, func(i, j int) bool { return in[i].Slug < in[j].Slug })
	return in, nil
}

var rootSlugs = map[string]bool{"index": true, "README": true, "readme": true, "home": true, "Home": true}

// Health reports orphans (no incoming links), broken wikilinks, and pages
// untouched for longer than staleDays.
func (s *Store) Health() (*Health, error) {
	metas, edges, broken, err := s.graphData()
	if err != nil {
		return nil, err
	}
	incoming := make(map[string]bool, len(edges))
	for _, e := range edges {
		incoming[e.To] = true
	}
	h := &Health{Broken: broken}
	for _, m := range metas {
		if !incoming[m.Slug] && !rootSlugs[m.Slug] {
			h.Orphans = append(h.Orphans, m)
		}
		if d := s.ageDays(m.Slug); d >= staleDays {
			h.Stale = append(h.Stale, StalePage{Slug: m.Slug, Title: m.Title, Days: d})
		}
	}
	sort.Slice(h.Stale, func(i, j int) bool { return h.Stale[i].Days > h.Stale[j].Days })
	return h, nil
}

// linkResolver maps a raw wikilink target to a canonical page slug the way
// Obsidian and GitHub wikis do: exact slug, then case-insensitive, then basename
// (shortest path breaking ties). It returns "" when nothing matches.
type linkResolver struct {
	exact map[string]string
	lower map[string]string
	base  map[string]string // lower(basename) -> slug
	norm  map[string]string // normalized basename/slug -> slug (space and hyphen folded)
}

func newLinkResolver(metas []PageMeta) *linkResolver {
	r := &linkResolver{
		exact: make(map[string]string, len(metas)),
		lower: make(map[string]string, len(metas)),
		base:  make(map[string]string, len(metas)),
		norm:  make(map[string]string, len(metas)*2),
	}
	put := func(m map[string]string, key, slug string) {
		if cur, ok := m[key]; !ok || shorterSlug(slug, cur) {
			m[key] = slug
		}
	}
	for _, m := range metas {
		r.exact[m.Slug] = m.Slug
		r.lower[strings.ToLower(m.Slug)] = m.Slug
		put(r.base, strings.ToLower(baseName(m.Slug)), m.Slug)
		put(r.norm, normalize(m.Slug), m.Slug)
		put(r.norm, normalize(baseName(m.Slug)), m.Slug)
	}
	return r
}

func (r *linkResolver) resolve(target string) string {
	if s, ok := r.exact[target]; ok {
		return s
	}
	if s, ok := r.lower[strings.ToLower(target)]; ok {
		return s
	}
	if s, ok := r.base[strings.ToLower(baseName(target))]; ok {
		return s
	}
	if s, ok := r.norm[normalize(target)]; ok {
		return s
	}
	if s, ok := r.norm[normalize(baseName(target))]; ok {
		return s
	}
	return ""
}

// normalize folds case and maps spaces to hyphens, so a link "Authoring Content"
// reaches a page filed as "authoring-content", the way Obsidian/GitHub slugify.
func normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.ReplaceAll(s, " ", "-")
}

type wlink struct {
	target string
	embed  bool // preceded by !, an image or note transclusion
}

// wikiLinks returns the [[...]] references in body, flagging embeds (![[...]]).
func wikiLinks(body string) []wlink {
	locs := wikiLinkRe.FindAllStringSubmatchIndex(body, -1)
	out := make([]wlink, 0, len(locs))
	for _, loc := range locs {
		out = append(out, wlink{
			target: body[loc[2]:loc[3]],
			embed:  loc[0] > 0 && body[loc[0]-1] == '!',
		})
	}
	return out
}

var (
	fenceRe   = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~")
	icodeRe   = regexp.MustCompile("`[^`\n]*`")
	mdLinkRe  = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)`)
	assetExts = []string{".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".pdf", ".mp4", ".mov", ".webm", ".canvas", ".base", ".excalidraw"}
)

// markdownLinkTargets returns the internal page targets of [text](target) links,
// skipping images, anchors, external URLs, and asset files. Targets are cleaned
// to a slug candidate (no ./, no .md, no #fragment) for the resolver.
func markdownLinkTargets(body string) []string {
	var out []string
	for _, loc := range mdLinkRe.FindAllStringSubmatchIndex(body, -1) {
		if loc[0] > 0 && body[loc[0]-1] == '!' {
			continue // image
		}
		t := body[loc[2]:loc[3]]
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "mailto:") || strings.Contains(t, "://") {
			continue
		}
		if i := strings.IndexByte(t, '#'); i >= 0 {
			t = t[:i]
		}
		t = strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(t, "./"), "/"), ".md")
		if t == "" || isAssetRef(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// stripCode removes fenced and inline code so wikilink syntax shown as an
// example in documentation is not mistaken for a real link.
func stripCode(body string) string {
	return icodeRe.ReplaceAllString(fenceRe.ReplaceAllString(body, ""), "")
}

// resolveTarget resolves a raw [[...]] target. It strips a #anchor, and for a
// piped target tries both sides, so it handles Obsidian [[target|label]] and
// Dendron/GitHub [[label|target]] alike.
func resolveTarget(r *linkResolver, raw string) string {
	raw = unescapePipe(raw)
	parts := []string{raw}
	if i := strings.Index(raw, "|"); i >= 0 {
		parts = []string{raw[:i], raw[i+1:]}
	}
	for _, c := range parts {
		if i := strings.Index(c, "#"); i >= 0 {
			c = c[:i]
		}
		if c = strings.TrimSpace(c); c != "" {
			if s := r.resolve(c); s != "" {
				return s
			}
		}
	}
	return ""
}

// unescapePipe normalizes a wikilink target so a table-escaped pipe (\|) or an
// HTML-entity pipe (&#124;) is treated as the | separator, the way these appear
// inside Obsidian table cells.
func unescapePipe(s string) string {
	return strings.NewReplacer(`\|`, "|", "&#124;", "|", "&#x7c;", "|", "&#x7C;", "|").Replace(s)
}

// isAssetRef reports whether a target points at an asset file rather than a
// page, so a missing one is not counted as a broken page link.
func isAssetRef(target string) bool {
	t := target
	if i := strings.IndexAny(t, "|#"); i >= 0 {
		t = t[:i]
	}
	t = strings.ToLower(strings.TrimSpace(t))
	for _, ext := range assetExts {
		if strings.HasSuffix(t, ext) {
			return true
		}
	}
	return false
}

// brokenName is the cleaned target shown in the broken-link report.
// brokenName is the target reported for an unresolved wikilink: the side before
// the pipe (the Obsidian convention), with an escaped pipe normalized and the
// #anchor stripped. A bare #anchor link yields "" and is not reported.
func brokenName(raw string) string {
	t := unescapePipe(raw)
	if i := strings.Index(t, "|"); i >= 0 {
		t = t[:i]
	}
	if i := strings.Index(t, "#"); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(t)
}

func baseName(slug string) string {
	if i := strings.LastIndex(slug, "/"); i >= 0 {
		return slug[i+1:]
	}
	return slug
}

// shorterSlug reports whether a is the better basename match than b: fewer path
// segments first, then alphabetical, so ties resolve deterministically.
func shorterSlug(a, b string) bool {
	na, nb := strings.Count(a, "/"), strings.Count(b, "/")
	if na != nb {
		return na < nb
	}
	return a < b
}

// ResolveLink maps a wikilink target (which may carry a |alias or #anchor) to a
// known page slug, false when it resolves to no page.
func (s *Store) ResolveLink(target string) (string, bool) {
	metas, _, _, err := s.graphData()
	if err != nil {
		return "", false
	}
	canon := resolveTarget(newLinkResolver(metas), target)
	return canon, canon != ""
}

type Neighbor struct {
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	Distance int    `json:"distance"`
}

type Hub struct {
	Slug   string `json:"slug"`
	Title  string `json:"title"`
	Degree int    `json:"degree"`
}

type GraphNode struct {
	Slug   string `json:"slug"`
	Title  string `json:"title"`
	Degree int    `json:"degree"`
}

type GraphView struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// adjacency returns the page list, a slug->title map, and an undirected
// adjacency map: links are bidirectional for navigation, so A->B makes the two
// neighbors either way.
func (s *Store) adjacency() ([]PageMeta, map[string]string, map[string][]string, error) {
	metas, edges, _, err := s.graphData()
	if err != nil {
		return nil, nil, nil, err
	}
	s.graphMu.Lock()
	sig := s.graphSig
	s.graphMu.Unlock()

	s.adjMu.Lock()
	if sig == s.adjSig && s.adjMap != nil {
		m, t, a := s.adjMetas, s.adjTitle, s.adjMap
		s.adjMu.Unlock()
		return m, t, a, nil
	}
	s.adjMu.Unlock()

	title := make(map[string]string, len(metas))
	for _, m := range metas {
		title[m.Slug] = m.Title
	}
	adj := make(map[string][]string, len(metas))
	seen := make(map[string]bool, len(edges)*2)
	link := func(a, b string) {
		k := a + "\x00" + b
		if seen[k] {
			return
		}
		seen[k] = true
		adj[a] = append(adj[a], b)
	}
	for _, e := range edges {
		link(e.From, e.To)
		link(e.To, e.From)
	}
	s.adjMu.Lock()
	s.adjSig, s.adjMetas, s.adjTitle, s.adjMap = sig, metas, title, adj
	s.adjMu.Unlock()
	return metas, title, adj, nil
}

// Neighbors returns the pages reachable from slug within depth hops over the
// undirected link graph, nearest first, excluding the page itself.
func (s *Store) Neighbors(slug string, depth int) ([]Neighbor, error) {
	if depth < 1 {
		depth = 1
	}
	_, title, adj, err := s.adjacency()
	if err != nil {
		return nil, err
	}
	dist := map[string]int{slug: 0}
	queue := []string{slug}
	var out []Neighbor
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if dist[cur] >= depth {
			continue
		}
		for _, nb := range adj[cur] {
			if _, ok := dist[nb]; ok {
				continue
			}
			dist[nb] = dist[cur] + 1
			out = append(out, Neighbor{Slug: nb, Title: title[nb], Distance: dist[nb]})
			queue = append(queue, nb)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}

// Path returns the shortest chain of pages connecting from to to over the
// undirected link graph, inclusive of both ends, or nil if they are not
// connected.
func (s *Store) Path(from, to string) ([]PageMeta, error) {
	_, title, adj, err := s.adjacency()
	if err != nil {
		return nil, err
	}
	if _, ok := title[from]; !ok {
		return nil, nil
	}
	if from == to {
		return []PageMeta{{Slug: from, Title: title[from]}}, nil
	}
	prev := map[string]string{from: ""}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == to {
			break
		}
		for _, nb := range adj[cur] {
			if _, ok := prev[nb]; ok {
				continue
			}
			prev[nb] = cur
			queue = append(queue, nb)
		}
	}
	if _, ok := prev[to]; !ok {
		return nil, nil
	}
	var rev []PageMeta
	for at := to; at != ""; at = prev[at] {
		rev = append(rev, PageMeta{Slug: at, Title: title[at]})
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
}

// Hubs returns the n most-connected pages by undirected degree.
func (s *Store) Hubs(n int) ([]Hub, error) {
	metas, title, adj, err := s.adjacency()
	if err != nil {
		return nil, err
	}
	hubs := make([]Hub, 0, len(metas))
	for _, m := range metas {
		hubs = append(hubs, Hub{Slug: m.Slug, Title: title[m.Slug], Degree: len(adj[m.Slug])})
	}
	sort.Slice(hubs, func(i, j int) bool {
		if hubs[i].Degree != hubs[j].Degree {
			return hubs[i].Degree > hubs[j].Degree
		}
		return hubs[i].Slug < hubs[j].Slug
	})
	if n > 0 && len(hubs) > n {
		hubs = hubs[:n]
	}
	return hubs, nil
}

// GraphView returns the whole link graph as nodes (with degree) and edges, for
// rendering the visual map.
func (s *Store) GraphView() (*GraphView, error) {
	metas, edges, _, err := s.graphData()
	if err != nil {
		return nil, err
	}
	_, title, adj, err := s.adjacency()
	if err != nil {
		return nil, err
	}
	nodes := make([]GraphNode, 0, len(metas))
	for _, m := range metas {
		nodes = append(nodes, GraphNode{Slug: m.Slug, Title: title[m.Slug], Degree: len(adj[m.Slug])})
	}
	return &GraphView{Nodes: nodes, Edges: edges}, nil
}

func (s *Store) ageDays(slug string) int {
	p, err := s.pathFor(slug)
	if err != nil {
		return 0
	}
	info, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return int(time.Since(info.ModTime()).Hours() / 24)
}
