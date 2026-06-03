package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
