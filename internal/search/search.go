//go:build !zos && !nofts

package search

import (
	"database/sql"
	"strings"
	"sync"
	"unicode"

	_ "modernc.org/sqlite"

	"github.com/msradam/waqwaq/internal/store"
)

// Index is the SQLite FTS5 full-text index. It refreshes only when the store's
// mtime signature changes, and then updates only the pages that changed rather
// than rebuilding the whole table.
type Index struct {
	st  *store.Store
	db  *sql.DB
	mu  sync.Mutex
	sig string
	fps map[string]string // slug -> fingerprint of the indexed content
}

func New(st *store.Store) (*Index, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(0)
	db.SetConnMaxLifetime(0)
	if _, err := db.Exec(`CREATE VIRTUAL TABLE pages USING fts5(slug, title, body, tokenize='porter unicode61')`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Index{st: st, db: db}, nil
}

func (ix *Index) Close() error { return ix.db.Close() }

// Warm builds the index ahead of the first query so the first search does not
// pay the full-corpus indexing cost. Serve runs it in the background at startup.
func (ix *Index) Warm() {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	_ = ix.refresh()
}

func (ix *Index) Search(query string) ([]store.SearchHit, error) {
	match := buildMatch(query)
	if match == "" {
		return nil, nil
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if err := ix.refresh(); err != nil {
		return nil, err
	}
	rows, err := ix.db.Query(
		`SELECT slug, title, snippet(pages, 2, '', '', '…', 12) FROM pages WHERE pages MATCH ? ORDER BY rank LIMIT 50`,
		match)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []store.SearchHit
	for rows.Next() {
		var h store.SearchHit
		if err := rows.Scan(&h.Slug, &h.Title, &h.Snippet); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

func (ix *Index) refresh() error {
	sig, err := ix.st.Signature()
	if err != nil {
		return err
	}
	if sig == ix.sig {
		return nil
	}
	fps, err := ix.st.Fingerprints()
	if err != nil {
		return err
	}
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	del, err := tx.Prepare(`DELETE FROM pages WHERE slug = ?`)
	if err != nil {
		return err
	}
	defer del.Close()
	ins, err := tx.Prepare(`INSERT INTO pages(slug, title, body) VALUES(?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	for slug, fp := range fps {
		if ix.fps[slug] == fp {
			continue
		}
		page, err := ix.st.Read(slug)
		if err != nil {
			continue
		}
		if _, err := del.Exec(slug); err != nil {
			return err
		}
		if _, err := ins.Exec(slug, page.Title, page.Body); err != nil {
			return err
		}
	}
	for slug := range ix.fps {
		if _, ok := fps[slug]; !ok {
			if _, err := del.Exec(slug); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	ix.sig = sig
	ix.fps = fps
	return nil
}

// buildMatch turns free text into a safe FTS5 prefix-AND query. It splits on any
// non-alphanumeric run, so a qualified identifier like `sync.Pool` or `wiki_write`
// becomes its component prefix terms (`sync* pool*`), matching the way unicode61
// tokenizes the indexed body. Keeping only letters and digits also means
// arbitrary input cannot inject FTS operators.
func buildMatch(query string) string {
	var terms []string
	for _, field := range strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		terms = append(terms, strings.ToLower(field)+"*")
	}
	return strings.Join(terms, " ")
}
