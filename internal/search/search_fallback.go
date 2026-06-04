//go:build zos || nofts

package search

import (
	"errors"

	"github.com/msradam/waqwaq/internal/store"
)

// This build omits the SQLite FTS5 index. It is selected automatically on z/OS
// (where the modernc.org/sqlite driver does not build on s390x) and on demand
// anywhere with `-tags nofts`. New always fails, so callers fall back to the
// store's substring search; the type and methods exist only so code that
// references them still compiles.

type Index struct{}

func New(*store.Store) (*Index, error) {
	return nil, errors.New("FTS5 search is unavailable in this build; using substring search")
}

func (ix *Index) Close() error { return nil }

func (ix *Index) Search(string) ([]store.SearchHit, error) { return nil, nil }
