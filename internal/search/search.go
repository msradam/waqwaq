//go:build !zos && !nofts

package search

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS pagemeta(slug TEXT PRIMARY KEY, fp TEXT)`); err != nil {
		_ = db.Close()
		return nil, err
	}
	ix := &Index{st: st, db: db, fps: map[string]string{}}
	if persisted {
		ix.loadFingerprints() // an existing index already holds these pages
	}
	return ix, nil
}

// openIndexDB opens the persistent index in the OS cache directory (a derived
// cache, not part of the user's repo). It falls back to an in-memory database if
// the cache directory is unavailable or the on-disk file cannot be opened, so
// search always works even when persistence does not.
func openIndexDB(st *store.Store) (db *sql.DB, persisted bool, err error) {
	cache, cerr := os.UserCacheDir()
	if cerr == nil {
		// Resolve symlinks so every path form of the same physical wiki keys one
		// index (notably /tmp vs /private/tmp on macOS), letting serve and mcp
		// share the cache regardless of how the directory was named.
		root := st.Root()
		if resolved, rerr := filepath.EvalSymlinks(root); rerr == nil {
			root = resolved
		}
		key := sha256.Sum256([]byte(root))
		dir := filepath.Join(cache, "waqwaq", hex.EncodeToString(key[:8]))
		if os.MkdirAll(dir, 0o755) == nil {
			// search-v1 in the name lets a future schema change use a fresh file.
			if d, derr := sql.Open("sqlite", filepath.Join(dir, "search-v1.db")); derr == nil {
				if d.Ping() == nil {
					return d, true, nil
				}
				_ = d.Close()
			}
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
	setMeta, err := tx.Prepare(`INSERT INTO pagemeta(slug, fp) VALUES(?, ?) ON CONFLICT(slug) DO UPDATE SET fp = excluded.fp`)
	if err != nil {
		return err
	}
	defer setMeta.Close()
	delMeta, err := tx.Prepare(`DELETE FROM pagemeta WHERE slug = ?`)
	if err != nil {
		return err
	}
	defer delMeta.Close()
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
		if _, err := setMeta.Exec(slug, fp); err != nil {
			return err
		}
	}
	for slug := range ix.fps {
		if _, ok := fps[slug]; !ok {
			if _, err := del.Exec(slug); err != nil {
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
