package core

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Versioned reports whether this wiki is backed by a git repository, so callers
// can distinguish "no history because unversioned" from "no commits touch this
// page". Both otherwise surface as an empty History.
func (c *impl) Versioned() bool {
	return IsGitRepo(c.root)
}

// IsGitRepo reports whether root, or any parent directory, is a git repository.
// It lets callers distinguish "not versioned" from "versioned but no commits
// touch this page", both of which History reports as an empty history.
func IsGitRepo(root string) bool {
	_, err := git.PlainOpenWithOptions(root, &git.PlainOpenOptions{DetectDotGit: true})
	return err == nil
}

// History returns git commits that touched a page, newest first. It uses go-git
// (pure Go, no system git binary) so the binary stays self-contained and
// cross-compilable. If the wiki is not a git repo, it returns an empty history
// with no error, since a bare markdown folder is a valid wiki.
func (c *impl) History(ctx context.Context, slug string, limit int) ([]Commit, error) {
	if limit <= 0 {
		limit = 20
	}

	repo, err := git.PlainOpenWithOptions(c.root, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		if errors.Is(err, git.ErrRepositoryNotExists) {
			return []Commit{}, nil
		}
		return nil, err
	}

	// Path of the page relative to the repo working tree.
	wt, err := repo.Worktree()
	if err != nil {
		return nil, err
	}
	abs := c.pathFromSlug(slug)
	rel, err := filepath.Rel(wt.Filesystem.Root(), abs)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)

	head, err := repo.Head()
	if err != nil {
		// An empty repo (no commits yet) has no HEAD; that's an empty history.
		return []Commit{}, nil //nolint:nilerr // no-commits is not a failure
	}

	iter, err := repo.Log(&git.LogOptions{From: head.Hash(), FileName: &rel})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var commits []Commit
	for len(commits) < limit {
		com, err := iter.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		commits = append(commits, toCommit(com))
	}
	return commits, nil
}

func toCommit(com *object.Commit) Commit {
	msg := strings.TrimSpace(com.Message)
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i] // subject line only
	}
	return Commit{
		Hash:    com.Hash.String(),
		Author:  com.Author.Name,
		Date:    com.Author.When,
		Message: msg,
	}
}
