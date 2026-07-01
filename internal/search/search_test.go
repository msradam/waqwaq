package search

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSearchLiteral(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.md", "the quick brown fox")
	writeFile(t, dir, "sub/b.md", "lazy dog sleeps")
	writeFile(t, dir, "c.md", "nothing here")

	res, err := Search(dir, "lazy dog", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	if filepath.Base(res[0].Path) != "b.md" {
		t.Fatalf("want b.md, got %s", res[0].Path)
	}
	if res[0].Context == "" {
		t.Fatal("expected a context snippet")
	}
}

func TestSearchKeywordAND(t *testing.T) {
	dir := t.TempDir()
	// Words scattered, out of order, mixed case — a whole-string substring match
	// would miss this; keyword-AND finds it.
	writeFile(t, dir, "a.md", "The total daily OUTPUT value is summed per day.")
	writeFile(t, dir, "b.md", "output only, no total here")

	res, err := Search(dir, "output value per day", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || filepath.Base(res[0].Path) != "a.md" {
		t.Fatalf("keyword-AND should match only a.md, got %v", res)
	}

	// A term absent from every file yields nothing.
	none, _ := Search(dir, "output value nonexistentword", false, 10)
	if len(none) != 0 {
		t.Fatalf("a missing term should exclude the file, got %v", none)
	}
}

func TestSearchRegex(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.md", "order 12345 shipped")
	writeFile(t, dir, "b.md", "order abc not a number")

	res, err := Search(dir, `order \d+`, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || filepath.Base(res[0].Path) != "a.md" {
		t.Fatalf("regex should match only a.md, got %v", res)
	}
}

func TestSearchBadRegex(t *testing.T) {
	dir := t.TempDir()
	if _, err := Search(dir, "(unclosed", true, 10); err == nil {
		t.Fatal("expected compile error for bad regex")
	}
}

func TestSearchSkipsDotDirsAndBinary(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, ".git/config.md", "secret match target")
	writeFile(t, dir, "bin.md", "text\x00with nul match target")
	writeFile(t, dir, "ok.md", "match target here")

	res, err := Search(dir, "match target", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || filepath.Base(res[0].Path) != "ok.md" {
		t.Fatalf("want only ok.md, got %v", res)
	}
}

func TestSearchLimitAndSort(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"c.md", "a.md", "b.md"} {
		writeFile(t, dir, n, "common")
	}
	res, err := Search(dir, "common", false, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("want 2 (limit), got %d", len(res))
	}
	if filepath.Base(res[0].Path) != "a.md" || filepath.Base(res[1].Path) != "b.md" {
		t.Fatalf("results not sorted by path: %v", res)
	}
}
