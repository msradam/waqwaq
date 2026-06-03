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
	known := make(map[string]bool, len(metas))
	for _, m := range metas {
		known[m.Slug] = true
	}
	var edges []GraphEdge
	var broken []BrokenLink
	for _, m := range metas {
		page, err := s.Read(m.Slug)
		if err != nil {
			continue
		}
		seen := map[string]bool{}
		for _, match := range wikiLinkRe.FindAllStringSubmatch(page.Body, -1) {
			target := match[1]
			if i := strings.IndexAny(target, "|#"); i >= 0 {
				target = target[:i]
			}
			target = strings.TrimSpace(target)
			if target == "" || seen[target] {
				continue
			}
			seen[target] = true
			if known[target] {
				edges = append(edges, GraphEdge{From: m.Slug, To: target})
			} else {
				broken = append(broken, BrokenLink{From: m.Slug, To: target})
			}
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
