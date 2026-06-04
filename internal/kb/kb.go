// Package kb defines the read contract every Waqwaq surface targets. Both the
// local *store.Store and the remote *kbclient.Client satisfy KnowledgeBase, so
// surfaces depend on the contract, never on a backend.
package kb

import "github.com/msradam/waqwaq/internal/store"

type KnowledgeBase interface {
	List() ([]store.PageMeta, error)
	Read(slug string) (*store.Page, error)
	Search(query string) ([]store.SearchHit, error)
	GraphView() (*store.GraphView, error)
	Neighbors(slug string, depth int) ([]store.Neighbor, error)
	Path(from, to string) ([]store.PageMeta, error)
	Hubs(limit int) ([]store.Hub, error)
	Backlinks(slug string) ([]store.PageMeta, error)
	Health() (*store.Health, error)
	Tags() (map[string][]store.PageMeta, error)
	ResolveLink(target string) (string, bool)
}

var _ KnowledgeBase = (*store.Store)(nil)
