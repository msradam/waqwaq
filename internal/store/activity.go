package store

import (
	"fmt"
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
	Slug   string    `json:"slug"`
	Title  string    `json:"title"`
	Author string    `json:"author"`
	When   time.Time `json:"when"`
}

type Revision struct {
	Hash    string    `json:"hash"`
	Author  string    `json:"author"`
	Message string    `json:"message"`
	When    time.Time `json:"when"`
}

// Recent returns the n most recently changed pages, newest first.
func (s *Store) Recent(n int) ([]Change, error) {
	metas, err := s.List()
	if err != nil {
		return nil, err
	}
	var changes []Change
	for _, m := range metas {
		a, ok := s.LastTouched(m.Slug)
		if !ok {
			continue
		}
		changes = append(changes, Change{Slug: m.Slug, Title: m.Title, Author: a.Author, When: a.When})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].When.After(changes[j].When) })
	if len(changes) > n {
		changes = changes[:n]
	}
	return changes, nil
}

// History returns the git revisions of a single page, newest first.
func (s *Store) History(slug string) ([]Revision, error) {
	if !s.git {
		return nil, nil
	}
	p, err := s.pathFor(slug)
	if err != nil {
		return nil, err
	}
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
	var revs []Revision
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\x1f", 4)
		if len(parts) < 4 {
			continue
		}
		when, _ := time.Parse(time.RFC3339, parts[2])
		revs = append(revs, Revision{Hash: parts[0], Author: parts[1], When: when, Message: parts[3]})
	}
	return revs, nil
}

// Tags maps every tag to the pages that carry it in their frontmatter.
func (s *Store) Tags() (map[string][]PageMeta, error) {
	metas, err := s.List()
	if err != nil {
		return nil, err
	}
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
	return tags, nil
}

// FrontmatterTags reads the tags field, accepting either a YAML list or a
// comma-separated string.
func FrontmatterTags(fm map[string]any) []string {
	if fm == nil {
		return nil
	}
	var out []string
	switch v := fm["tags"].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}
