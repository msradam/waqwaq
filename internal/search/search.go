//go:build !zos && !nofts

package search

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"

	_ "modernc.org/sqlite"

	"github.com/msradam/waqwaq/internal/store"
)

// Index is the SQLite FTS5 full-text index. It persists to the OS cache directory
// keyed by the wiki's path, so it is built once and reused across serve and mcp
// invocations; each refresh updates only the pages whose fingerprint changed.
// This matters most for the per-session stdio mcp client, which would otherwise
// rebuild the whole index on every connection.
type Index struct {
	st  *store.Store
	db  *sql.DB
	mu  sync.Mutex
	sig string
	fps map[string]string // slug -> fingerprint of the indexed content
}

func New(st *store.Store) (*Index, error) {
	db, persisted, err := openIndexDB(st)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(0)
	db.SetConnMaxLifetime(0)
	// WAL lets a second process read while one refreshes; busy_timeout makes a
	// concurrent writer wait rather than error. No-ops on the in-memory fallback.
	for _, p := range []string{`PRAGMA journal_mode=WAL`, `PRAGMA busy_timeout=5000`, `PRAGMA synchronous=NORMAL`} {
		_, _ = db.Exec(p)
	}
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS pages USING fts5(slug, title, body, tokenize='porter unicode61')`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS pagemeta(slug TEXT PRIMARY KEY, fp TEXT, rid INTEGER)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	ix := &Index{st: st, db: db, fps: map[string]string{}}
	if persisted {
		ix.loadFingerprints() // an existing index already holds these pages
	}
	return ix, nil
}

// openIndexDB opens the persistent index in the wiki's cache directory (a
// derived cache, not part of the user's repo). It falls back to an in-memory
// database if the cache directory is unavailable or the on-disk file cannot be
// opened, so search always works even when persistence does not.
func openIndexDB(st *store.Store) (db *sql.DB, persisted bool, err error) {
	if dir, cerr := st.CacheDir(); cerr == nil {
		// search-v2 in the name lets a schema change use a fresh file; v1 (which
		// had no rowid column in pagemeta) is removed rather than left to rot.
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(filepath.Join(dir, "search-v1.db"+suffix))
		}
		if d, derr := sql.Open("sqlite", filepath.Join(dir, "search-v2.db")); derr == nil {
			if d.Ping() == nil {
				return d, true, nil
			}
			_ = d.Close()
		}
	}
	d, derr := sql.Open("sqlite", ":memory:")
	return d, false, derr
}

func (ix *Index) loadFingerprints() {
	rows, err := ix.db.Query(`SELECT slug, fp FROM pagemeta`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var slug, fp string
		if rows.Scan(&slug, &fp) == nil {
			ix.fps[slug] = fp
		}
	}
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
		`SELECT slug, title, snippet(pages, 2, '', '', '…', 12) FROM pages WHERE pages MATCH ? ORDER BY rank LIMIT ?`,
		match, store.SearchLimit+1) // one past, so the caller can detect truncation
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
	// Deleting an FTS5 row by column value full-scans the virtual table, which
	// made refreshes O(N²) in the page count (a cold 20k-page build took
	// minutes). Deletes go by rowid instead, tracked in pagemeta and read back
	// inside the transaction so a concurrent indexer's rows are still seen.
	del, err := tx.Prepare(`DELETE FROM pages WHERE rowid = ?`)
	if err != nil {
		return err
	}
	defer del.Close()
	ins, err := tx.Prepare(`INSERT INTO pages(slug, title, body) VALUES(?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	setMeta, err := tx.Prepare(`INSERT INTO pagemeta(slug, fp, rid) VALUES(?, ?, ?) ON CONFLICT(slug) DO UPDATE SET fp = excluded.fp, rid = excluded.rid`)
	if err != nil {
		return err
	}
	defer setMeta.Close()
	delMeta, err := tx.Prepare(`DELETE FROM pagemeta WHERE slug = ?`)
	if err != nil {
		return err
	}
	defer delMeta.Close()
	indexed, err := indexedRows(tx)
	if err != nil {
		return err
	}
	for slug, fp := range fps {
		rid, ok := indexed[slug]
		if ok && ix.fps[slug] == fp {
			continue
		}
		page, err := ix.st.Read(slug)
		if err != nil {
			continue
		}
		if ok {
			if _, err := del.Exec(rid); err != nil {
				return err
			}
		}
		res, err := ins.Exec(slug, page.Title, page.Body)
		if err != nil {
			return err
		}
		newRid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := setMeta.Exec(slug, fp, newRid); err != nil {
			return err
		}
	}
	for slug, rid := range indexed {
		if _, ok := fps[slug]; !ok {
			if _, err := del.Exec(rid); err != nil {
				return err
			}
			if _, err := delMeta.Exec(slug); err != nil {
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

// indexedRows maps each indexed slug to the rowid of its pages row.
func indexedRows(tx *sql.Tx) (map[string]int64, error) {
	rows, err := tx.Query(`SELECT slug, rid FROM pagemeta`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	indexed := make(map[string]int64)
	for rows.Next() {
		var slug string
		var rid sql.NullInt64
		if err := rows.Scan(&slug, &rid); err != nil {
			return nil, err
		}
		if rid.Valid {
			indexed[slug] = rid.Int64
		}
	}
	return indexed, rows.Err()
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
