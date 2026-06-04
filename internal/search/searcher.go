// Package search provides full-text search over the wiki. The default build uses
// a SQLite FTS5 index (pure-Go modernc.org/sqlite, no cgo). On platforms that
// driver does not support, such as z/OS (s390x, big-endian), the build excludes
// it and the caller falls back to the store's substring scan, which also
// satisfies Searcher.
package search

import "github.com/msradam/waqwaq/internal/store"

// Searcher is the search backend. Both *Index and *store.Store implement it.
type Searcher interface {
	Search(query string) ([]store.SearchHit, error)
}
