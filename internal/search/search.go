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

// Index is the SQLite FTS5 full-text index, rebuilt only when the store's mtime
// signature changes.
type Index struct {
	st  *store.Store
	db  *sql.DB
	mu  sync.Mutex
	sig string
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
	metas, err := ix.st.List()
	if err != nil {
		return err
	}
	tx, err := ix.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM pages`); err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO pages(slug, title, body) VALUES(?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, m := range metas {
		page, err := ix.st.Read(m.Slug)
		if err != nil {
			continue
		}
		if _, err := stmt.Exec(m.Slug, m.Title, page.Body); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	ix.sig = sig
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
