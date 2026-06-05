package store

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var revRe = regexp.MustCompile(`^[0-9a-fA-F]{4,40}(~[0-9]+)?$`)

// ReadAtRev returns a page's content at a git revision (a commit hash, optionally
// with a ~N parent suffix). It returns "" when the file did not exist there.
func (s *Store) ReadAtRev(slug, ref string) (string, error) {
	if !s.git {
		return "", nil
	}
	if !revRe.MatchString(ref) {
		return "", fmt.Errorf("invalid revision %q", ref)
	}
	p, err := s.pathFor(slug)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(s.gitRoot, p)
	if err != nil {
		return "", err
	}
	cmd := exec.Command("git", "show", ref+":"+rel)
	cmd.Dir = s.gitRoot
	out, err := cmd.Output()
	if err != nil {
		return "", nil // file did not exist at that revision
	}
	return string(out), nil
}

// RevExists reports whether ref resolves to a commit in the repo.
func (s *Store) RevExists(ref string) bool {
	if !s.git || !revRe.MatchString(ref) {
		return false
	}
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	cmd.Dir = s.gitRoot
	return cmd.Run() == nil
}

type Change struct {
	Slug    string    `json:"slug"`
	Title   string    `json:"title"`
	Author  string    `json:"author"`
	When    time.Time `json:"when"`
	Deleted bool      `json:"deleted,omitempty"`
}

type Revision struct {
	Hash    string    `json:"hash"`
	Author  string    `json:"author"`
	Message string    `json:"message"`
	When    time.Time `json:"when"`
}

// Recent returns the n most recently changed pages, newest first. It reads the
// recent commits with a single streaming `git log` and stops as soon as it has
// n distinct pages, so its cost is independent of the total page count (the old
// approach forked one `git log` per page).
func (s *Store) Recent(n int) ([]Change, error) {
	if n <= 0 {
		n = 20
	}
	metas, err := s.List()
	if err != nil {
		return nil, err
	}
	title := make(map[string]string, len(metas))
	for _, m := range metas {
		title[m.Slug] = m.Title
	}
	if !s.git {
		return s.recentByModTime(metas, title, n), nil
	}

	pagesRel := filepath.ToSlash(s.relPages())
	// core.quotePath=false keeps non-ASCII (unicode/emoji) slugs unquoted so the
	// path parses; --name-status marks deletions (D) so they appear in the feed.
	cmd := exec.Command("git", "-c", "core.quotePath=false", "log", "-n", "2000", "--name-status", "--format=%x01%an%x1f%aI")
	cmd.Dir = s.gitRoot
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var changes []Change
	seen := make(map[string]bool)
	var author string
	var when time.Time
	for sc.Scan() {
		line := sc.Text()
		if len(line) > 0 && line[0] == '\x01' {
			parts := strings.SplitN(line[1:], "\x1f", 2)
			author = parts[0]
			if len(parts) > 1 {
				when, _ = time.Parse(time.RFC3339, parts[1])
			}
			continue
		}
		if line == "" {
			continue
		}
		// --name-status lines are "<status>\t<path>", or "<status>\t<old>\t<new>"
		// for renames; the current path is the last field.
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		slug, ok := slugForGitPath(fields[len(fields)-1], pagesRel)
		if !ok || seen[slug] {
			continue
		}
		seen[slug] = true
		t, known := title[slug]
		if !known {
			t = slug // a deleted (or renamed-away) page has no current title
		}
		changes = append(changes, Change{
			Slug: slug, Title: t, Author: author, When: when,
			Deleted: strings.HasPrefix(fields[0], "D"),
		})
		if len(changes) >= n {
			break
		}
	}
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].When.After(changes[j].When) })
	return changes, nil
}

// relPages is the page directory relative to the git root ("." when they are the
// same directory), matching the paths `git log` prints.
func (s *Store) relPages() string {
	rel, err := filepath.Rel(s.gitRoot, s.pages)
	if err != nil {
		return "."
	}
	return rel
}

// slugForGitPath turns a git-reported path into a page slug, or reports false if
// it is not a markdown page under the page directory.
func slugForGitPath(f, pagesRel string) (string, bool) {
	if !strings.HasSuffix(f, ".md") {
		return "", false
	}
	f = strings.TrimSuffix(f, ".md")
	if pagesRel == "." || pagesRel == "" {
		return f, true
	}
	pre := pagesRel + "/"
	if !strings.HasPrefix(f, pre) {
		return "", false
	}
	return strings.TrimPrefix(f, pre), true
}

// recentByModTime is the fallback ordering when the store is not a git repo.
func (s *Store) recentByModTime(metas []PageMeta, title map[string]string, n int) []Change {
	type entry struct {
		slug, title string
		when        time.Time
	}
	all := make([]entry, 0, len(metas))
	for _, m := range metas {
		p, err := s.pathFor(m.Slug)
		if err != nil {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		all = append(all, entry{m.Slug, title[m.Slug], fi.ModTime()})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].when.After(all[j].when) })
	if len(all) > n {
		all = all[:n]
	}
	out := make([]Change, len(all))
	for i, e := range all {
		out[i] = Change{Slug: e.slug, Title: e.title, When: e.when}
	}
	return out
}

// History returns the git revisions of a page, newest first. A deleted page
// still surfaces its revisions, since its content stays recoverable from history.
// It returns os.ErrNotExist only when the page neither exists nor has any history
// (so a caller can tell a never-created slug from an existing untracked one).
func (s *Store) History(slug string) ([]Revision, error) {
	p, err := s.pathFor(slug)
	if err != nil {
		return nil, err
	}
	var revs []Revision
	if s.git {
		rel, err := filepath.Rel(s.gitRoot, p)
		if err != nil {
			return nil, err
		}
		cmd := exec.Command("git", "log", "--format=%h%x1f%an%x1f%aI%x1f%s", "-n", "50", "--", rel)
		cmd.Dir = s.gitRoot
		out, err := cmd.Output()
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			parts := strings.SplitN(line, "\x1f", 4)
			if len(parts) < 4 {
				continue
			}
			when, _ := time.Parse(time.RFC3339, parts[2])
			revs = append(revs, Revision{Hash: parts[0], Author: parts[1], When: when, Message: parts[3]})
		}
	}
	if len(revs) > 0 {
		return revs, nil
	}
	if _, err := os.Stat(p); err != nil {
		return nil, fmt.Errorf("page %q not found: %w", slug, os.ErrNotExist)
	}
	return nil, nil
}

// Tags maps every tag to the pages that carry it in their frontmatter, cached
// by Signature so the per-page frontmatter reads happen once per change.
func (s *Store) Tags() (map[string][]PageMeta, error) {
	metas, err := s.List()
	if err != nil {
		return nil, err
	}
	sig, err := s.Signature()
	if err != nil {
		return nil, err
	}
	s.tagsMu.Lock()
	if sig == s.tagsSig && s.tagsCache != nil {
		t := s.tagsCache
		s.tagsMu.Unlock()
		return t, nil
	}
	s.tagsMu.Unlock()

	tags := map[string][]PageMeta{}
	for _, m := range metas {
		page, err := s.Read(m.Slug)
		if err != nil {
			continue
		}
		for _, t := range FrontmatterTags(page.Frontmatter) {
			tags[t] = append(tags[t], m)
		}
	}
	s.tagsMu.Lock()
	s.tagsSig, s.tagsCache = sig, tags
	s.tagsMu.Unlock()
	return tags, nil
}

// FrontmatterTags reads the tags field, accepting either a YAML list or a
// comma-separated string.
// FrontmatterTags collects a page's tags from the frontmatter: the `tags` field
// (a list or a comma string), Hugo's `categories`, and the values of a Hugo/Zola
// `[taxonomies]` table.
func FrontmatterTags(fm map[string]any) []string {
	if fm == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(v any) {
		var vals []string
		switch t := v.(type) {
		case []any:
			for _, x := range t {
				if s, ok := x.(string); ok {
					vals = append(vals, s)
				}
			}
		case string:
			vals = strings.Split(t, ",")
		}
		for _, s := range vals {
			if s = strings.TrimSpace(s); s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	add(fm["tags"])
	add(fm["categories"])
	switch tax := fm["taxonomies"].(type) {
	case map[string]any:
		for _, v := range tax {
			add(v)
		}
	case map[any]any:
		for _, v := range tax {
			add(v)
		}
	}
	return out
}
