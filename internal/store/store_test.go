package store

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeleteUntrackedIsRecoverable(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.git {
		t.Skip("git unavailable")
	}
	// Drop a file straight into the pages dir (vault-style) so it is untracked.
	const token = "vaultrecovertoken"
	p := filepath.Join(s.Pages(), "dropped.md")
	if err := os.WriteFile(p, []byte("---\ntitle: D\n---\n"+token+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("dropped", "", "remove dropped"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Error("file was not removed from disk")
	}
	cmd := exec.Command("git", "log", "-S", token, "--format=%H")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(out)) == "" {
		t.Error("deleted untracked file is not recoverable from git history")
	}
}

func TestDelete(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Write("folder/page", "---\ntitle: P\n---\nbody\n", "", "add"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("folder/page", "", "del"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Read("folder/page"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("after Delete, Read err = %v, want ErrNotExist", err)
	}
	if _, err := os.Stat(filepath.Join(s.Pages(), "folder")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("empty parent folder was not pruned")
	}
	if err := s.Delete("folder/page", "", "del"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Delete of a missing page = %v, want ErrNotExist", err)
	}
	if err := s.Delete("../escape", "", "del"); err == nil {
		t.Error("Delete of a path-traversal slug should be refused")
	}
}

func TestRecentUnicodeAndDeletions(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !s.git {
		t.Skip("git unavailable")
	}
	write := func(slug string) {
		if err := s.Write(slug, "---\ntitle: "+slug+"\n---\nbody\n", "", "add "+slug); err != nil {
			t.Fatal(err)
		}
	}
	write("ascii")
	uni := "café-🌳-日本語"
	write(uni)
	write("doomed")
	if err := s.Delete("doomed", "", "remove doomed"); err != nil {
		t.Fatal(err)
	}
	changes, err := s.Recent(20)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]Change, len(changes))
	for _, c := range changes {
		got[c.Slug] = c
	}
	if _, ok := got[uni]; !ok {
		t.Errorf("Recent dropped the unicode-slug page %q", uni)
	}
	if d, ok := got["doomed"]; !ok {
		t.Error("Recent dropped the deleted page")
	} else if !d.Deleted {
		t.Error("deleted page in Recent is not marked Deleted")
	}
}

func TestFrontmatterBodyWithThematicBreak(t *testing.T) {
	fm, body := SplitFrontmatter("---\ntitle: T\n---\n\nintro\n\n---\n\nmore\n")
	if fm["title"] != "T" {
		t.Fatalf("title = %v, want T", fm["title"])
	}
	if !strings.Contains(body, "---") {
		t.Errorf("body lost its thematic break: %q", body)
	}
	if !strings.Contains(body, "more") {
		t.Errorf("body was truncated: %q", body)
	}
}

func TestSplitFrontmatter(t *testing.T) {
	fm, body := SplitFrontmatter("---\ntitle: Hello\n---\n\n# Hi\n")
	if fm["title"] != "Hello" {
		t.Fatalf("title = %v, want Hello", fm["title"])
	}
	if body != "# Hi\n" {
		t.Fatalf("body = %q, want %q", body, "# Hi\n")
	}

	fm, body = SplitFrontmatter("# No frontmatter\n")
	if fm != nil {
		t.Fatalf("expected nil frontmatter, got %v", fm)
	}
	if body != "# No frontmatter\n" {
		t.Fatalf("body changed: %q", body)
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := "---\ntitle: Alpha\n---\n\nBody text.\n"
	if err := s.Write("notes/alpha", content, "", "test"); err != nil {
		t.Fatal(err)
	}
	page, err := s.Read("notes/alpha")
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "Alpha" {
		t.Errorf("title = %q, want Alpha", page.Title)
	}
	if page.Slug != "notes/alpha" {
		t.Errorf("slug = %q, want notes/alpha", page.Slug)
	}
	metas, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Slug != "notes/alpha" {
		t.Errorf("list = %+v", metas)
	}
}

func TestPathTraversalRejected(t *testing.T) {
	s, _ := New(t.TempDir())
	for _, bad := range []string{"../escape", "raw/secret", "../../etc/passwd"} {
		if _, err := s.pathFor(bad); err == nil {
			t.Errorf("pathFor(%q) = nil error, want rejection", bad)
		}
	}
	if _, err := s.pathFor("sources"); err != nil {
		t.Errorf(`pathFor("sources") should be allowed, got %v`, err)
	}
}

func TestLayoutDetection(t *testing.T) {
	if s, _ := New(t.TempDir()); s.Layout() != "folder" {
		t.Errorf("flat layout = %q, want folder", s.Layout())
	}
	withWiki := t.TempDir()
	if err := os.MkdirAll(filepath.Join(withWiki, "wiki"), 0o755); err != nil {
		t.Fatal(err)
	}
	if s, _ := New(withWiki); s.Layout() != "wiki/" {
		t.Errorf("wiki layout = %q, want wiki/", s.Layout())
	}
}

func TestGraphResolvesKnownEdgesOnly(t *testing.T) {
	s, _ := New(t.TempDir())
	if err := s.Write("a", "---\ntitle: A\n---\nlink to [[b]] and [[missing]].\n", "", "m"); err != nil {
		t.Fatal(err)
	}
	if err := s.Write("b", "---\ntitle: B\n---\nback to [[a|A]].\n", "", "m"); err != nil {
		t.Fatal(err)
	}
	_, edges, err := s.Graph()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range edges {
		got[e.From+"->"+e.To] = true
	}
	for _, want := range []string{"a->b", "b->a"} {
		if !got[want] {
			t.Errorf("missing edge %s; got %v", want, got)
		}
	}
	if got["a->missing"] {
		t.Errorf("edge to unknown page should be dropped; got %v", got)
	}
}
