// Package tui is the terminal reader: an interactive shell over a read-only
// core.Core, browsing a local OKF wiki folder.
package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/msradam/waqwaq/core"
)

type item struct{ slug, title string }

func (i item) Title() string       { return i.title }
func (i item) Description() string { return i.slug }
func (i item) FilterValue() string { return i.slug + " " + i.title }

func toItems(metas []core.PageMeta) []list.Item {
	its := make([]list.Item, len(metas))
	for i, m := range metas {
		its[i] = item{slug: m.Slug, title: m.Title}
	}
	return its
}

type Model struct {
	core     core.Core
	list     list.Model
	vp       viewport.Model
	search   textinput.Model
	all      []list.Item
	cur      string
	curBody  string
	curEdges []core.EdgeRef // outbound+inbound of the current page, for 'r' and the footer
	body     string         // rendered page body, before the related footer is appended
	focus    int            // 0 list, 1 content
	typing   bool
	w, h     int
	ready    bool // a window size is known
	loaded   bool // the page list has arrived
	opened   bool // the initial page has been opened
	status   string
	style    string // glamour style: "dark" or "light", detected once
	renderer *glamour.TermRenderer
	renderW  int
}

// pagesMsg carries the page list loaded off the render loop.
type (
	pagesMsg struct{ items []list.Item }
	errMsg   struct{ err error }
)

// New builds the TUI model over a read-only Core.
func New(c core.Core) (Model, error) {
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Loading…"
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	ti := textinput.New()
	ti.Placeholder = "full-text search"
	return Model{core: c, list: l, vp: viewport.New(0, 0), search: ti, status: "Loading pages…"}, nil
}

func (m Model) Init() tea.Cmd {
	c := m.core
	return func() tea.Msg {
		res, err := c.List(context.Background(), "", "", "", 0, 0)
		if err != nil {
			return errMsg{err}
		}
		return pagesMsg{toItems(res.Items)}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.layout()
		if !m.ready {
			m.ready = true
			cmds = append(cmds, m.maybeOpenInitial())
		} else if m.cur != "" {
			m.render(m.cur)
		}
	case pagesMsg:
		m.loaded = true
		m.all = msg.items
		m.list.SetItems(msg.items)
		if len(msg.items) == 0 {
			m.list.Title = "No pages"
			m.status = "no markdown pages found here"
		} else {
			m.list.Title = "Pages"
		}
		cmds = append(cmds, m.maybeOpenInitial())
	case errMsg:
		m.loaded = true
		m.status = msg.err.Error()
	case tea.KeyMsg:
		if m.typing {
			switch msg.String() {
			case "enter":
				m.typing = false
				m.search.Blur()
				m.runSearch(m.search.Value())
			case "esc":
				m.typing = false
				m.search.Blur()
			default:
				var cmd tea.Cmd
				m.search, cmd = m.search.Update(msg)
				cmds = append(cmds, cmd)
			}
			return m, tea.Batch(cmds...)
		}
		if m.focus == 0 && m.list.FilterState() == list.Filtering {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "tab":
			m.focus = 1 - m.focus
		case "s":
			m.typing = true
			m.search.SetValue("")
			m.search.Focus()
			return m, textinput.Blink
		case "a":
			m.list.SetItems(m.all)
			m.list.Title = "Pages"
			m.focus = 0
		case "r":
			if m.cur != "" {
				m.loadRelated(m.cur)
			}
		case "enter", "l":
			if m.focus == 0 {
				if it, ok := m.list.SelectedItem().(item); ok {
					m.open(it.slug)
					m.focus = 1
				}
			}
		default:
			var cmd tea.Cmd
			if m.focus == 0 {
				m.list, cmd = m.list.Update(msg)
			} else {
				m.vp, cmd = m.vp.Update(msg)
			}
			cmds = append(cmds, cmd)
		}
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) layout() {
	listW := 36
	if m.w < 96 {
		listW = m.w / 3
	}
	bodyH := m.h - 1
	m.list.SetSize(listW, bodyH)
	m.vp.Width = m.w - listW - 3
	m.vp.Height = bodyH - 1 // one line for the content-pane header
}

// maybeOpenInitial opens the landing page once both the window size and the page
// list are known.
func (m *Model) maybeOpenInitial() tea.Cmd {
	if !m.ready || !m.loaded || m.opened || len(m.all) == 0 {
		return nil
	}
	m.opened = true
	// index.md is the OKF traversal entry point. It is reserved, so it is not in
	// the concept list; open it by name, falling back to the first concept.
	if !m.open("index") {
		m.open(m.all[0].(item).slug)
	}
	return nil
}

// open reads a page and renders it, reporting whether it succeeded. Read inlines
// the page's outbound and inbound edges, so the related footer and the 'r' walk
// need no extra calls.
func (m *Model) open(slug string) bool {
	page, err := m.core.Read(context.Background(), slug)
	if err != nil {
		m.status = "not found: " + slug
		return false
	}
	m.cur, m.curBody = page.Slug, page.Body
	m.curEdges = mergeEdges(page.Outbound, page.Inbound)
	m.render(m.cur)
	return true
}

// mergeEdges combines outbound and inbound edges, de-duplicating by slug and
// preserving order (outbound first).
func mergeEdges(outbound, inbound []core.EdgeRef) []core.EdgeRef {
	seen := make(map[string]bool)
	var out []core.EdgeRef
	for _, group := range [][]core.EdgeRef{outbound, inbound} {
		for _, e := range group {
			if !seen[e.Slug] {
				seen[e.Slug] = true
				out = append(out, e)
			}
		}
	}
	return out
}

// ensureRenderer builds the Glamour renderer once per width. WithStandardStyle
// avoids the per-render terminal query WithAutoStyle does, which stalls inside
// the Bubble Tea alt-screen.
func (m *Model) ensureRenderer() {
	width := m.vp.Width - 2
	if width < 20 {
		width = 20
	}
	if m.renderer != nil && m.renderW == width {
		return
	}
	style := m.style
	if style == "" {
		style = "dark"
	}
	if r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(width)); err == nil {
		m.renderer, m.renderW = r, width
	}
}

func (m *Model) render(slug string) {
	m.ensureRenderer()
	out := m.curBody
	if m.renderer != nil {
		if s, err := m.renderer.Render(m.curBody); err == nil {
			out = s
		}
	}
	m.body = out
	m.vp.SetContent(out + m.relatedFooter())
	m.vp.GotoTop()
	m.status = slug
}

// relatedFooter renders the current page's inlined edges as a footer, split into
// outbound and inbound sections.
func (m *Model) relatedFooter() string {
	if len(m.curEdges) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n  ── related (press r to walk) ──\n")
	for _, e := range m.curEdges {
		b.WriteString("  • " + e.Slug + "\n")
	}
	return b.String()
}

// loadRelated repoints the list at the current page's linked neighbourhood,
// using the edges already inlined by the last Read.
func (m *Model) loadRelated(slug string) {
	if len(m.curEdges) == 0 {
		m.status = "no related pages for " + slug
		return
	}
	its := make([]list.Item, 0, len(m.curEdges))
	for _, e := range m.curEdges {
		its = append(its, item{slug: e.Slug, title: e.Title})
	}
	m.list.SetItems(its)
	m.list.Title = "related: " + slug
	m.focus = 0
}

func (m *Model) runSearch(q string) {
	if strings.TrimSpace(q) == "" {
		return
	}
	hits, err := m.core.Search(context.Background(), q, false, "", "", 0)
	if err != nil {
		m.status = err.Error()
		return
	}
	its := make([]list.Item, 0, len(hits))
	for _, h := range hits {
		its = append(its, item{slug: h.Slug, title: h.Title})
	}
	m.list.SetItems(its)
	m.list.Title = "search: " + q
	m.focus = 0
}

var (
	hintStyle  = lipgloss.NewStyle().Faint(true)
	focusStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	blurStyle  = lipgloss.NewStyle().Faint(true)
)

func paneTitle(label string, focused bool) string {
	mark := "  "
	style := blurStyle
	if focused {
		mark, style = "▌ ", focusStyle
	}
	return style.Render(mark + label)
}

func (m Model) View() string {
	if !m.ready {
		return "loading…"
	}
	listFocus := m.focus == 0
	m.list.Styles.Title = focusStyle
	if !listFocus {
		m.list.Styles.Title = blurStyle
	}
	cur := m.cur
	if cur == "" {
		cur = "(no page open)"
	}
	right := paneTitle(cur, !listFocus) + "\n" + m.vp.View()
	body := lipgloss.JoinHorizontal(lipgloss.Top, m.list.View(), "  ", right)

	var footer string
	if m.typing {
		footer = "  search: " + m.search.View()
	} else {
		where := "list"
		if !listFocus {
			where = "content"
		}
		footer = hintStyle.Render("  [" + where + "]  ↑↓ move · enter open · tab focus · / filter · s search · r related · a all · q quit")
	}
	return body + "\n" + footer
}

// Run starts the terminal reader over c and blocks until the user quits.
func Run(c core.Core) error {
	m, err := New(c)
	if err != nil {
		return err
	}
	// Detect the terminal background once, before the program takes over the
	// terminal, so the renderer never has to query it mid-frame.
	m.style = "dark"
	if !lipgloss.HasDarkBackground() {
		m.style = "light"
	}
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
