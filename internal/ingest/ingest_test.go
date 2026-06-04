package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoImportGraph(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/m\n\ngo 1.21\n")
	write("a/a.go", "// Package a is the top, and it mentions [[wikilinks]] in prose.\npackage a\n\nimport _ \"example.com/m/b\"\n")
	write("b/b.go", "// Package b is the base.\npackage b\n")

	out := t.TempDir()
	n, err := Go(dir, out)
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("pages = %d, want >= 2", n)
	}
	page, err := os.ReadFile(filepath.Join(out, "wiki", "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(page)
	if !strings.Contains(s, "[[b|b]]") {
		t.Errorf("a.md missing real import wikilink to b:\n%s", s)
	}
	if !strings.Contains(s, "Package a is the top") {
		t.Errorf("a.md missing the package doc comment")
	}
	if strings.Contains(s, "[[wikilinks]]") {
		t.Errorf("doc-comment prose [[wikilinks]] should be neutralised, not emitted as a wikilink")
	}
}
