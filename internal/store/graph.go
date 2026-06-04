package store

import (
	"os"
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

// graphData builds (and caches by mtime signature) the link graph: pages,
// resolved [[wikilink]] edges, and broken links pointing at missing pages. It is
// recomputed only when a page changes, so backlinks and health stay cheap.
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
		seenTo := map[string]bool{}
		for _, match := range wikiLinkRe.FindAllStringSubmatch(page.Body, -1) {
			target := match[1]
			if i := strings.IndexAny(target, "|#"); i >= 0 {
				target = target[:i]
			}
			target = strings.TrimSpace(target)
			if target == "" {
				continue
			}
			canon := resolver.resolve(target)
			if canon == "" {
				broken = append(broken, BrokenLink{From: m.Slug, To: target})
				continue
			}
			if canon == m.Slug || seenTo[canon] {
				continue
			}
			seenTo[canon] = true
			edges = append(edges, GraphEdge{From: m.Slug, To: canon})
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

// Health reports pages that need attention: orphans (no incoming links), broken
// wikilinks, and pages untouched for longer than staleDays.
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
// Obsidian and GitHub wikis do: an exact slug wins, then a case-insensitive
// exact match, then a basename match (the page with that file name anywhere in
// the tree, the shortest path breaking ties). It returns "" when nothing matches.
type linkResolver struct {
	exact map[string]string
	lower map[string]string
	base  map[string]string
}

func newLinkResolver(metas []PageMeta) *linkResolver {
	r := &linkResolver{
		exact: make(map[string]string, len(metas)),
		lower: make(map[string]string, len(metas)),
		base:  make(map[string]string, len(metas)),
	}
	for _, m := range metas {
		r.exact[m.Slug] = m.Slug
		r.lower[strings.ToLower(m.Slug)] = m.Slug
		key := strings.ToLower(baseName(m.Slug))
		if cur, ok := r.base[key]; !ok || shorterSlug(m.Slug, cur) {
			r.base[key] = m.Slug
		}
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
	return ""
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
// known page slug, matching the wiki's link graph. It returns false when the
// target does not resolve to any page.
func (s *Store) ResolveLink(target string) (string, bool) {
	metas, _, _, err := s.graphData()
	if err != nil {
		return "", false
	}
	if i := strings.IndexAny(target, "|#"); i >= 0 {
		target = target[:i]
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return "", false
	}
	canon := newLinkResolver(metas).resolve(target)
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
// adjacency map over resolved links. Links are treated as bidirectional for
// navigation: if A references B, the two are neighbors either way.
func (s *Store) adjacency() ([]PageMeta, map[string]string, map[string][]string, error) {
	metas, edges, _, err := s.graphData()
	if err != nil {
		return nil, nil, nil, err
	}
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
// connected. It is how an agent answers "how does X relate to Y".
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

// Hubs returns the n most-connected pages by undirected degree, the natural
// entry points into an unfamiliar wiki.
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
