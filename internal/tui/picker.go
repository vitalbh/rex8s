// Package tui is the fuzzy picker with a preview pane, built with Bubble Tea.
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sahilm/fuzzy"
)

// Item is one row of the picker.
type Item struct {
	Key   string // returned value (e.g. context name)
	Label string // main text, filterable
	Hint  string // gray text on the right, also filterable
	Badge string // "PROD", "dev" or ""
	Dim   bool   // special option (e.g. "context default")
}

// Options configures a picker.
type Options struct {
	Title    string
	Items    []Item
	Load     func() ([]Item, error) // used when Items is empty: loads in the background
	Selected string                 // Key selected initially
	Query    string                 // initial filter text

	// Optional preview: Head is instant, Body runs in the background (may be slow).
	PreviewHead func(Item) string
	PreviewBody func(Item) string
	Prefetch    bool // fetch the Body of every item up front

	// Multi: space marks items, ctrl+a marks every item the filter shows;
	// enter returns the marked ones (or the one under the cursor). Use RunMulti.
	Multi bool
}

// Run opens the picker full screen. Returns nil if the user cancels.
func Run(o Options) (*Item, error) {
	res, err := tea.NewProgram(newModel(o), tea.WithAltScreen()).Run()
	if err != nil {
		return nil, err
	}
	return res.(model).choice, nil
}

// RunMulti is Run with marking (Options.Multi): returns the marked items, in
// list order, or the one under the cursor when none is marked. Nil on cancel.
func RunMulti(o Options) ([]Item, error) {
	o.Multi = true
	res, err := tea.NewProgram(newModel(o), tea.WithAltScreen()).Run()
	if err != nil {
		return nil, err
	}
	return res.(model).chosen, nil
}

// ── model ──────────────────────────────────────────────────────────────

type match struct {
	idx int
	pos map[int]bool // matched byte positions in Label
}

type itemsMsg struct {
	items []Item
	err   error
}

type previewMsg struct{ key, body string }

type model struct {
	o       Options
	items   []Item
	matches []match
	cursor  int
	offset  int
	input   textinput.Model
	spin    spinner.Model
	loading bool
	err     error
	cache   map[string]string
	pending map[string]bool
	w, h    int
	choice  *Item
	marked  map[string]bool
	chosen  []Item
}

func newModel(o Options) model {
	ti := textinput.New()
	ti.Prompt = "  "
	ti.Placeholder = "type to filter"
	ti.PromptStyle = accent
	ti.Focus()
	ti.SetValue(o.Query)

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = accent

	m := model{
		o: o, items: o.Items, input: ti, spin: sp,
		cache: map[string]string{}, pending: map[string]bool{}, marked: map[string]bool{},
		w: 80, h: 24,
	}
	m.loading = len(o.Items) == 0 && o.Load != nil
	m.refilter()
	m.selectKey(o.Selected)
	return m
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink, m.spin.Tick}
	if m.loading {
		load := m.o.Load
		cmds = append(cmds, func() tea.Msg { it, err := load(); return itemsMsg{it, err} })
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		return m, m.wantPreviews()

	case itemsMsg:
		m.loading, m.items, m.err = false, msg.items, msg.err
		m.refilter()
		m.selectKey(m.o.Selected)
		return m, m.wantPreviews()

	case previewMsg:
		m.cache[msg.key] = msg.body
		delete(m.pending, msg.key)
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "enter":
			if len(m.matches) > 0 {
				it := m.items[m.matches[m.cursor].idx]
				m.choice = &it
				m.chosen = m.markedItems()
				if len(m.chosen) == 0 {
					m.chosen = []Item{it}
				}
				return m, tea.Quit
			}
			return m, nil
		case " ":
			if m.o.Multi {
				if len(m.matches) > 0 {
					k := m.items[m.matches[m.cursor].idx].Key
					m.marked[k] = !m.marked[k]
					m.move(1)
				}
				return m, m.wantPreviews()
			}
		case "ctrl+a":
			if m.o.Multi {
				all := true
				for _, mt := range m.matches {
					all = all && m.marked[m.items[mt.idx].Key]
				}
				for _, mt := range m.matches {
					m.marked[m.items[mt.idx].Key] = !all
				}
				return m, nil
			}
		case "up", "ctrl+p", "ctrl+k":
			m.move(-1)
			return m, m.wantPreviews()
		case "down", "ctrl+n", "ctrl+j", "tab":
			m.move(1)
			return m, m.wantPreviews()
		case "pgup":
			m.move(-m.listHeight())
			return m, m.wantPreviews()
		case "pgdown":
			m.move(m.listHeight())
			return m, m.wantPreviews()
		}
		before := m.input.Value()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		if m.input.Value() != before {
			m.refilter()
			m.cursor, m.offset = 0, 0
		}
		return m, tea.Batch(cmd, m.wantPreviews())
	}
	return m, nil
}

func (m model) markedItems() []Item {
	var out []Item
	for _, it := range m.items {
		if m.marked[it.Key] {
			out = append(out, it)
		}
	}
	return out
}

func (m *model) move(d int) {
	if len(m.matches) == 0 {
		return
	}
	m.cursor += d
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.matches) {
		m.cursor = len(m.matches) - 1
	}
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
}

func (m *model) selectKey(key string) {
	if key == "" {
		return
	}
	for i, mt := range m.matches {
		if m.items[mt.idx].Key == key {
			m.cursor = i
			return
		}
	}
}

type source []Item

func (s source) String(i int) string { return s[i].Label + " " + s[i].Hint }
func (s source) Len() int            { return len(s) }

func (m *model) refilter() {
	q := strings.TrimSpace(m.input.Value())
	m.matches = m.matches[:0]
	if q == "" {
		for i := range m.items {
			m.matches = append(m.matches, match{idx: i})
		}
		return
	}
	for _, r := range fuzzy.FindFrom(q, source(m.items)) {
		pos := map[int]bool{}
		for _, p := range r.MatchedIndexes {
			pos[p] = true
		}
		m.matches = append(m.matches, match{idx: r.Index, pos: pos})
	}
}

// wantPreviews fetches the preview of the current item (or all, with Prefetch).
func (m model) wantPreviews() tea.Cmd {
	if m.o.PreviewBody == nil || len(m.matches) == 0 {
		return nil
	}
	var targets []Item
	if m.o.Prefetch {
		targets = m.items
	} else {
		targets = []Item{m.items[m.matches[m.cursor].idx]}
	}
	var cmds []tea.Cmd
	for _, it := range targets {
		if _, ok := m.cache[it.Key]; ok || m.pending[it.Key] {
			continue
		}
		m.pending[it.Key] = true // map: the change is visible in the returned model
		it, body := it, m.o.PreviewBody
		cmds = append(cmds, func() tea.Msg { return previewMsg{it.Key, body(it)} })
	}
	return tea.Batch(cmds...)
}

// ── view ───────────────────────────────────────────────────────────────

var (
	accent   = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	dim      = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	bold     = lipgloss.NewStyle().Bold(true)
	hiMatch  = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	selRow   = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	prodTag  = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("160")).Bold(true)
	devTag   = lipgloss.NewStyle().Foreground(lipgloss.Color("40"))
	errStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	box      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
)

func (m model) listHeight() int {
	h := m.h - 5 // title, help, input, blank line, footer
	if h < 3 {
		h = 3
	}
	return h
}

func (m model) showPreview() bool { return m.o.PreviewHead != nil && m.w >= 84 }

func (m model) View() string {
	var b strings.Builder
	title := bold.Render(m.o.Title)
	help := "↑/↓ move · enter select · esc quit · type to filter"
	if m.o.Multi {
		if n := len(m.markedItems()); n > 0 {
			title += "  " + selRow.Render(fmt.Sprintf("%d marked", n))
		}
		help = "space mark · ctrl+a mark all shown · enter go with the marked (or this one) · esc quit · type to filter"
	}
	b.WriteString(title + "\n")
	b.WriteString(dim.Render(help) + "\n")
	b.WriteString(m.input.View() + "\n\n")

	listW := m.w
	if m.showPreview() {
		listW = m.w * 45 / 100
		if listW < 38 {
			listW = 38
		}
	}
	list := m.renderList(listW)
	if m.showPreview() && len(m.matches) > 0 {
		pw := m.w - listW - 1
		pv := box.Width(pw - 2).Height(m.listHeight() - 2).MaxHeight(m.listHeight()).Render(m.renderPreview(pw - 4))
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, list, " ", pv))
	} else {
		b.WriteString(list)
	}
	return b.String()
}

func (m model) renderList(width int) string {
	h := m.listHeight()
	switch {
	case m.loading:
		return lipgloss.NewStyle().Width(width).Height(h).Render("  " + m.spin.View() + " loading...")
	case m.err != nil:
		return lipgloss.NewStyle().Width(width).Height(h).Render("  " + errStyle.Render(m.err.Error()))
	case len(m.matches) == 0:
		return lipgloss.NewStyle().Width(width).Height(h).Render("  " + dim.Render("(no matches)"))
	}

	off := m.offset
	if m.cursor < off {
		off = m.cursor
	}
	if m.cursor >= off+h {
		off = m.cursor - h + 1
	}
	var rows []string
	for i := off; i < len(m.matches) && i < off+h; i++ {
		rows = append(rows, m.renderRow(m.matches[i], i == m.cursor, width))
	}
	return lipgloss.NewStyle().Width(width).Height(h).MaxWidth(width).Render(strings.Join(rows, "\n"))
}

// hasBadges: some item shows a badge, so badge-less rows get its width.
func (m model) hasBadges() bool {
	for _, it := range m.items {
		if it.Badge != "" {
			return true
		}
	}
	return false
}

func (m model) renderRow(mt match, selected bool, width int) string {
	it := m.items[mt.idx]
	cur := "  "
	if selected {
		cur = selRow.Render("> ")
	}
	tag := ""
	switch it.Badge {
	case "PROD":
		tag = prodTag.Render("PROD") + " "
	case "FAIL":
		tag = errStyle.Bold(true).Render("FAIL") + " "
	case "SUSP", "WAIT":
		tag = Warn.Render(padRight(it.Badge, 4)) + " "
	case "":
		if m.hasBadges() { // keep the columns aligned with the rows that have one
			tag = "     "
		}
	default:
		tag = devTag.Render(padRight(it.Badge, 4)) + " "
	}

	base := lipgloss.NewStyle()
	switch {
	case it.Dim:
		base = dim
	case selected:
		base = selRow
	}
	var label strings.Builder
	for i, r := range it.Label {
		s := string(r)
		if mt.pos[i] {
			label.WriteString(hiMatch.Render(s))
		} else {
			label.WriteString(base.Render(s))
		}
	}
	mark := ""
	if m.o.Multi {
		mark = dim.Render("[ ] ")
		if m.marked[it.Key] {
			mark = selRow.Render("[x] ")
		}
	}
	row := cur + mark + tag + label.String()
	if it.Hint != "" {
		row += "  " + dim.Render(it.Hint)
	}
	return lipgloss.NewStyle().MaxWidth(width).Render(row)
}

func (m model) renderPreview(width int) string {
	it := m.items[m.matches[m.cursor].idx]
	head := m.o.PreviewHead(it)
	if m.o.PreviewBody == nil {
		return head
	}
	body, ok := m.cache[it.Key]
	if !ok {
		body = m.spin.View() + dim.Render(" querying cluster...")
	}
	rule := dim.Render(strings.Repeat("─", max(width, 1)))
	return head + "\n" + rule + "\n" + body
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

// Exported styles for callers building preview content.
var (
	Dim   = dim
	Bold  = bold
	Err   = errStyle
	Ok    = lipgloss.NewStyle().Foreground(lipgloss.Color("40"))
	Warn  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	Prod  = prodTag
	Muted = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
)
