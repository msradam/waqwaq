// Package search is a SQLite FTS5 full-text index over the wiki, kept fresh by
// rebuilding only when the store's mtime signature changes. It uses the pure-Go
// modernc.org/sqlite driver, so there is no cgo and cross-compilation stays
// trivial. Store also satisfies Searcher with a substring scan, which is the
// fallback when FTS5 is unavailable.
package search

import (
	"database/sql"
	"strings"
	"sync"
	"unicode"

	_ "modernc.org/sqlite"

	"github.com/msradam/waqwaq/internal/store"
)

// Searcher is the search backend. Both *Index and *store.Store implement it.
type Searcher interface {
	Search(query string) ([]store.SearchHit, error)
}

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

// buildMatch turns free text into a safe FTS5 prefix-AND query. It keeps only
// letters and digits per token, so arbitrary input cannot inject FTS operators.
func buildMatch(query string) string {
	var terms []string
	for _, field := range strings.Fields(query) {
		clean := strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}
			return -1
		}, field)
		if clean != "" {
			terms = append(terms, clean+"*")
		}
	}
	return strings.Join(terms, " ")
}
