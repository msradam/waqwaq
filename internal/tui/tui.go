// Package tui is the terminal reader: an interactive shell over the same
// kb.KnowledgeBase the CLI verbs use, against a local folder or a remote server
// without knowing which.
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/msradam/waqwaq/internal/kb"
	"github.com/msradam/waqwaq/internal/store"
)

type item struct{ slug, title string }

func (i item) Title() string       { return i.title }
func (i item) Description() string { return i.slug }
func (i item) FilterValue() string { return i.slug + " " + i.title }

func toItems(metas []store.PageMeta) []list.Item {
	its := make([]list.Item, len(metas))
	for i, m := range metas {
		its[i] = item{slug: m.Slug, title: m.Title}
	}
	return its
}

type Model struct {
	base     kb.KnowledgeBase
	list     list.Model
	vp       viewport.Model
	search   textinput.Model
	all      []list.Item
	cur      string
	curBody  string
	body     string // rendered page body, before the async related footer is appended
	focus    int    // 0 list, 1 content
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

// pagesMsg carries the page list loaded off the render loop; footerMsg carries a
// page's backlinks and neighbors, computed off the render loop because the first
// such call builds the whole link graph.
type (
	pagesMsg  struct{ items []list.Item }
	footerMsg struct {
		slug string
		text string
	}
	errMsg struct{ err error }
)

func New(base kb.KnowledgeBase) (Model, error) {
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Loading…"
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	ti := textinput.New()
	ti.Placeholder = "full-text search"
	return Model{base: base, list: l, vp: viewport.New(0, 0), search: ti, status: "Loading pages…"}, nil
}

func (m Model) Init() tea.Cmd {
	base := m.base
	return func() tea.Msg {
		metas, err := base.List()
		if err != nil {
			return errMsg{err}
		}
		return pagesMsg{toItems(metas)}
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
			cmds = append(cmds, m.footerCmd(m.cur))
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
	case footerMsg:
		if msg.slug == m.cur {
			m.vp.SetContent(m.body + msg.text)
		}
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
					cmds = append(cmds, m.open(it.slug))
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
// list are known, returning the command that loads its related footer.
func (m *Model) maybeOpenInitial() tea.Cmd {
	if !m.ready || !m.loaded || m.opened || len(m.all) == 0 {
		return nil
	}
	m.opened = true
	slug := m.all[0].(item).slug
	for _, it := range m.all {
		if it.(item).slug == "index" {
			slug = "index"
			break
		}
	}
	return m.open(slug)
}

func (m *Model) open(slug string) tea.Cmd {
	page, err := m.base.Read(slug)
	if err != nil {
		if canon, ok := m.base.ResolveLink(slug); ok {
			page, err = m.base.Read(canon)
		}
	}
	if err != nil || page == nil {
		m.status = "not found: " + slug
		return nil
	}
	m.cur, m.curBody = page.Slug, page.Body
	m.render(m.cur)
	return m.footerCmd(m.cur)
}

// footerCmd computes a page's backlinks and neighbors off the render loop, since
// the first such call on a cold store builds the entire link graph.
func (m *Model) footerCmd(slug string) tea.Cmd {
	base := m.base
	return func() tea.Msg {
		return footerMsg{slug: slug, text: relatedFooter(base, slug)}
	}
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
	m.vp.SetContent(out)
	m.vp.GotoTop()
	m.status = slug
}

func relatedFooter(base kb.KnowledgeBase, slug string) string {
	var b strings.Builder
	if bl, _ := base.Backlinks(slug); len(bl) > 0 {
		b.WriteString("\n  ── backlinks ──\n")
		for _, p := range bl {
			b.WriteString("  ← " + p.Slug + "\n")
		}
	}
	if nb, _ := base.Neighbors(slug, 1); len(nb) > 0 {
		b.WriteString("\n  ── related (press r to walk) ──\n")
		for _, n := range nb {
			b.WriteString("  • " + n.Slug + "\n")
		}
	}
	return b.String()
}

func (m *Model) loadRelated(slug string) {
	seen := map[string]bool{slug: true}
	var its []list.Item
	add := func(s, t string) {
		if !seen[s] {
			seen[s] = true
			its = append(its, item{slug: s, title: t})
		}
	}
	if nb, _ := m.base.Neighbors(slug, 1); nb != nil {
		for _, n := range nb {
			add(n.Slug, n.Title)
		}
	}
	if bl, _ := m.base.Backlinks(slug); bl != nil {
		for _, p := range bl {
			add(p.Slug, p.Title)
		}
	}
	if len(its) == 0 {
		m.status = "no related pages for " + slug
		return
	}
	m.list.SetItems(its)
	m.list.Title = "related: " + slug
	m.focus = 0
}

func (m *Model) runSearch(q string) {
	if strings.TrimSpace(q) == "" {
		return
	}
	hits, err := m.base.Search(q)
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

// Run starts the terminal reader over base and blocks until the user quits.
func Run(base kb.KnowledgeBase) error {
	m, err := New(base)
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
