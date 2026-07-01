// Package search is a small ripgrep-style scanner over a directory of markdown.
// Literal queries fast-path through bytes.Contains; pattern queries use stdlib
// regexp (RE2: linear-time, no catastrophic backtracking). The directory walk is
// parallelized across a goroutine pool.
//
// ponytail: out of scope by design — SIMD/Teddy multi-literal prefiltering and
// mmap I/O. Wiki-scale corpora (thousands of small files) are dominated by walk
// and read syscalls, not match throughput; don't add either without a benchmark
// that motivates it.
package search

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Result is one matching file with a context snippet around the first hit.
type Result struct {
	Path    string
	Context string
}

// contextRadius is how many bytes of surrounding text to include in a snippet.
const contextRadius = 60

// Search scans dir for query. When isRegex is true the query is compiled as a
// stdlib regexp. Otherwise the query is split into whitespace-separated terms and
// a file matches when it contains ALL terms, case-insensitively (keyword AND, not
// a whole-string substring) — so a natural multi-word query like "output value
// per day" finds pages carrying those words, which a single-substring match would
// miss. Results are capped at limit (0 means a default of 100) and returned
// sorted by path for determinism.
func Search(dir, query string, isRegex bool, limit int) ([]Result, error) {
	if limit <= 0 {
		limit = 100
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	var re *regexp.Regexp
	var terms [][]byte // lowercased keyword terms for the literal AND path
	if isRegex {
		var err error
		re, err = regexp.Compile(query)
		if err != nil {
			return nil, err
		}
	} else {
		for _, t := range strings.Fields(query) {
			terms = append(terms, []byte(strings.ToLower(t)))
		}
	}

	paths, err := collectFiles(dir)
	if err != nil {
		return nil, err
	}

	results := scan(paths, func(data []byte) (int, bool) {
		if isRegex {
			loc := re.FindIndex(data)
			if loc == nil {
				return 0, false
			}
			return loc[0], true
		}
		// ponytail: lowercase a copy per file for case-insensitive AND. Fine at
		// wiki scale; if a corpus ever dwarfs this, fold case during the walk.
		lower := bytes.ToLower(data)
		first := -1
		for _, term := range terms {
			idx := bytes.Index(lower, term)
			if idx < 0 {
				return 0, false // a required term is absent
			}
			if first < 0 || idx < first {
				first = idx
			}
		}
		return first, true
	})

	sort.Slice(results, func(i, j int) bool { return results[i].Path < results[j].Path })
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// collectFiles walks dir for .md files, skipping dotfiles and dot-directories
// (which covers .git, .waqwaq, etc.) so we never need an external ignore lib.
func collectFiles(dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != dir && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	return paths, err
}

// scan reads each path concurrently and, for matches, builds a context snippet.
// match reports the byte offset of the first hit and whether the file matched.
func scan(paths []string, match func([]byte) (int, bool)) []Result {
	workers := runtime.NumCPU()
	if workers > len(paths) {
		workers = len(paths)
	}
	if workers < 1 {
		workers = 1
	}

	jobs := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var results []Result

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				data, err := os.ReadFile(p)
				if err != nil {
					continue
				}
				// Skip binary files the way ripgrep/git do: a NUL byte in the
				// first read window means "not text".
				if window := data; len(window) > 8192 {
					window = window[:8192]
					if bytes.IndexByte(window, 0) >= 0 {
						continue
					}
				} else if bytes.IndexByte(window, 0) >= 0 {
					continue
				}
				off, ok := match(data)
				if !ok {
					continue
				}
				mu.Lock()
				results = append(results, Result{Path: p, Context: snippet(data, off)})
				mu.Unlock()
			}
		}()
	}
	for _, p := range paths {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	return results
}

// snippet returns a single-line context window around off, collapsed to one
// line. When the window is cut mid-word at either edge, it snaps to the nearest
// space so the snippet does not start or end inside a word.
func snippet(data []byte, off int) string {
	start := off - contextRadius
	if start < 0 {
		start = 0
	} else if i := bytes.IndexByte(data[start:off], ' '); i >= 0 {
		start += i + 1 // drop the leading partial word
	}
	end := off + contextRadius
	if end > len(data) {
		end = len(data)
	} else if i := bytes.LastIndexByte(data[off:end], ' '); i >= 0 {
		end = off + i // drop the trailing partial word
	}
	s := string(data[start:end])
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}
