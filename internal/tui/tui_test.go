package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/msradam/waqwaq/internal/store"
)

func key(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestModelOpensAndWalksGraph(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(slug, content string) {
		if err := st.Write(slug, content, "", "m"); err != nil {
			t.Fatal(err)
		}
	}
	write("index", "---\ntitle: Home\n---\nsee [[guide]]\n")
	write("guide", "---\ntitle: Guide\n---\nback to [[index]]\n")

	m, err := New(st)
	if err != nil {
		t.Fatal(err)
	}

	// The first window size makes it ready and auto-opens the index page.
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	mm := model.(Model)
	if !mm.ready || mm.cur != "index" {
		t.Fatalf("ready=%v cur=%q, want ready index", mm.ready, mm.cur)
	}
	if v := mm.View(); v == "" || v == "loading…" {
		t.Fatal("empty view after open")
	}

	// 'r' repoints the list at the current page's graph neighbourhood.
	model, _ = mm.Update(key("r"))
	mm = model.(Model)
	if mm.list.Title != "related: index" {
		t.Fatalf("list title = %q, want 'related: index'", mm.list.Title)
	}

	// 'q' quits.
	if _, cmd := mm.Update(key("q")); cmd == nil {
		t.Fatal("q should return a command")
	}
}
