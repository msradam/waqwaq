package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/msradam/waqwaq/core"
)

func key(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// fixture writes a small wiki to a temp dir and returns a Core over it.
func fixture(t *testing.T) core.Core {
	t.Helper()
	dir := t.TempDir()
	write := func(slug, content string) {
		p := filepath.Join(dir, "wiki", slug+".md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index", "---\ntitle: Home\n---\nsee [[guide]]\n")
	write("guide", "---\ntitle: Guide\n---\nback to [[index]]\n")

	c, err := core.New(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestModelOpensAndWalksGraph(t *testing.T) {
	m, err := New(fixture(t))
	if err != nil {
		t.Fatal(err)
	}

	// The page list loads off the render loop; run that command and deliver it.
	model, _ := m.Update(m.Init()())
	mm := model.(Model)
	// index.md is reserved, so the concept list is just guide; index still opens
	// as the entry page by name.
	if !mm.loaded || len(mm.all) != 1 {
		t.Fatalf("loaded=%v pages=%d, want 1 concept page loaded", mm.loaded, len(mm.all))
	}

	// The first window size makes it ready and auto-opens the index page.
	model, _ = mm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	mm = model.(Model)
	if !mm.ready || mm.cur != "index" {
		t.Fatalf("ready=%v cur=%q, want ready index", mm.ready, mm.cur)
	}
	if v := mm.View(); v == "" || v == "loading…" {
		t.Fatal("empty view after open")
	}

	// index links out to guide, so its inlined edges drive the 'r' walk.
	if len(mm.curEdges) == 0 {
		t.Fatal("expected inlined edges on the opened page")
	}

	// 'r' repoints the list at the current page's graph neighbourhood.
	model, _ = mm.Update(key("r"))
	mm = model.(Model)
	if mm.list.Title != "related: index" {
		t.Fatalf("list title = %q, want 'related: index'", mm.list.Title)
	}

	if _, cmd := mm.Update(key("q")); cmd == nil {
		t.Fatal("q should return a command")
	}
}

func TestSearchRepointsList(t *testing.T) {
	m, err := New(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	model, _ := m.Update(m.Init()())
	mm := model.(Model)
	model, _ = mm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	mm = model.(Model)

	mm.runSearch("guide")
	if mm.list.Title != "search: guide" {
		t.Fatalf("list title = %q, want 'search: guide'", mm.list.Title)
	}
}
