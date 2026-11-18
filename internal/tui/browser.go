package tui

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Browser is a k9s-like screen: one tab per kind of object, each a list that
// refreshes by itself; letters run actions on the row under the cursor ("/"
// filters), and text results (describe, YAML…) open in a viewer.

// Action is a key that does something with the selected row.
type Action struct {
	Key     string // as Bubble Tea names it: "l", "ctrl+d", "enter"
	Label   string // shown in the help line
	Confirm bool   // changes something: asks y/N first (production still asks again)
}

// View is one tab.
type View struct {
	Name   string
	Header func() string // column titles, aligned with the rows' Label (after a load)
	// Load runs in the background and returns the tab's rows. While it runs
	// it may call partial with the rows so far (all of them, not only the
	// new ones): a view of many clusters shows each one as it answers.
	Load    func(partial func([]Item)) ([]Item, error)
	Every   time.Duration // refresh interval (0: only on R)
	Actions []Action
}

// Result is what an action asks the browser to do next.
type Result struct {
	Status    string    // a line at the bottom
	Err       error     // shown at the bottom, in red
	Text      string    // open the viewer with this
	TextTitle string    //
	Exec      *exec.Cmd // run with the terminal (prompts, shells), then come back
	Goto      int       // switch to this tab (index+1; 0: stay)
	Filter    string    // with Goto: the text filter there
	// Only, with Goto: show only the rows it accepts there (exact, unlike
	// the text filter), described by OnlyLabel ("pods of deploy/web"). esc
	// clears it.
	Only      func(Item) bool
	OnlyLabel string
	Reload    bool // reload this tab now
	Quit      bool
}

// BrowserOptions configures a browser.
type BrowserOptions struct {
	Title  string
	Views  []View
	Start  int
	Filter string                                     // the starting tab's filter
	Action func(view int, it Item, key string) Result // runs in the background
	// Reload, when set, runs on R before the tab loads again (to retry
	// clusters that were skipped, say).
	Reload func()
}

// RunBrowser opens the browser full screen until q.
func RunBrowser(o BrowserOptions) error {
	_, err := tea.NewProgram(newBrowser(o), tea.WithAltScreen()).Run()
	return err
}

type tabState struct {
	items     []Item
	err       error
	ready     bool      // a load answered (in part, at least): rows or an error to show
	loading   bool      //
	again     bool      // asked to load while loading: load once more when it ends
	stale     bool      // its refresh came while hidden: load when it is shown
	loadedAt  time.Time // when the last load ended
	gen       int
	filter    string
	only      func(Item) bool // Result.Only
	onlyLabel string
	cursor    int
	offset    int
	key       string // the selected row's Key, kept across refreshes
	moved     bool   // the cursor was moved; until then a first load's partial rows keep it on top
}

// What the keys do.
const (
	modeList   = iota // move, act on the selected row
	modeFilter        // typing the filter
	modeText          // reading an action's text
)

type browser struct {
	o       BrowserOptions
	tabs    []tabState
	cur     int
	mode    int // modeList, modeFilter or modeText
	input   textinput.Model
	spin    spinner.Model
	busy    string // an action is running
	status  string
	statErr bool
	confirm *pending // a Confirm action waiting for y
	text    []string
	textTtl string
	scroll  int
	w, h    int
}

type pending struct {
	view int
	it   Item
	act  Action
}

type (
	loadedMsg struct {
		tab, gen int
		items    []Item
		err      error
	}
	partialMsg struct {
		tab, gen int
		items    []Item
		s        *loadStream
	}
	refreshMsg struct{ tab, gen int }
	actionMsg  Result
	execMsg    struct{ err error }
)

func newBrowser(o BrowserOptions) browser {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.PromptStyle = accent
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = accent
	b := browser{o: o, tabs: make([]tabState, len(o.Views)), cur: o.Start, input: ti, spin: sp, w: 100, h: 30}
	if o.Start < len(b.tabs) {
		b.tabs[o.Start].filter = o.Filter
	}
	return b
}

func (b browser) Init() tea.Cmd { return tea.Batch(b.spin.Tick, b.load(b.cur)) }

// loadStream carries one load's partial rows (only the latest matter) and
// its end to the browser. Sending never blocks.
type loadStream struct {
	mu     sync.Mutex
	latest []Item
	ready  chan struct{}  // cap 1: latest changed
	done   chan loadedMsg // cap 1
}

func newLoadStream() *loadStream {
	return &loadStream{ready: make(chan struct{}, 1), done: make(chan loadedMsg, 1)}
}

func (s *loadStream) partial(items []Item) {
	s.mu.Lock()
	s.latest = items
	s.mu.Unlock()
	select {
	case s.ready <- struct{}{}:
	default: // already signalled: the reader takes the latest
	}
}

// next waits for the load's next partial rows or for its end.
func (s *loadStream) next(tab, gen int) tea.Cmd {
	return func() tea.Msg {
		select {
		case m := <-s.done:
			return m
		case <-s.ready:
			s.mu.Lock()
			items := s.latest
			s.mu.Unlock()
			return partialMsg{tab, gen, items, s}
		}
	}
}

// load starts loading a tab. A tab loads once at a time: asked again while
// it loads (repeated R), it loads once more when the running load ends.
func (b *browser) load(tab int) tea.Cmd {
	t := &b.tabs[tab]
	if t.loading {
		t.again = true
		return nil
	}
	t.gen++
	t.loading, t.stale, t.again = true, false, false
	gen, fn := t.gen, b.o.Views[tab].Load
	s := newLoadStream()
	run := func() tea.Msg {
		items, err := fn(s.partial)
		s.done <- loadedMsg{tab, gen, items, err}
		return nil
	}
	return tea.Batch(run, s.next(tab, gen))
}

func (b browser) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		b.w, b.h = msg.Width, msg.Height
		return b, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		b.spin, cmd = b.spin.Update(msg)
		return b, cmd

	case partialMsg:
		t := &b.tabs[msg.tab]
		if msg.gen != t.gen || !t.loading {
			return b, nil
		}
		t.items, t.err, t.ready = msg.items, nil, true
		if t.loadedAt.IsZero() && !t.moved { // first rows still coming in sorted: stay on top
			t.key, t.cursor = "", 0
		}
		b.follow(msg.tab)
		return b, msg.s.next(msg.tab, msg.gen)

	case loadedMsg:
		t := &b.tabs[msg.tab]
		if msg.gen != t.gen {
			return b, nil
		}
		if t.loadedAt.IsZero() && !t.moved { // the first rows: on top, whichever cluster answered first
			t.key, t.cursor = "", 0
		}
		t.loading, t.ready, t.loadedAt = false, true, time.Now()
		if msg.err != nil && len(t.items) > 0 {
			t.err = msg.err // keep the last good rows; say the refresh failed
		} else {
			t.items, t.err = msg.items, msg.err
		}
		b.follow(msg.tab)
		if t.again {
			return b, b.load(msg.tab)
		}
		if every := b.o.Views[msg.tab].Every; every > 0 {
			tab, gen := msg.tab, t.gen
			return b, tea.Tick(every, func(time.Time) tea.Msg { return refreshMsg{tab, gen} })
		}
		return b, nil

	case refreshMsg:
		t := &b.tabs[msg.tab]
		if msg.gen != t.gen || t.loading {
			return b, nil
		}
		if msg.tab != b.cur { // loads when shown again; its rows stay meanwhile
			t.stale = true
			return b, nil
		}
		return b, b.load(msg.tab)

	case actionMsg:
		b.busy = ""
		return b.apply(Result(msg))

	case execMsg:
		if msg.err != nil {
			b.status, b.statErr = msg.err.Error(), true
		}
		return b, b.load(b.cur)

	case tea.KeyMsg:
		switch b.mode {
		case modeFilter:
			return b.keyFilter(msg)
		case modeText:
			return b.keyText(msg)
		}
		return b.keyList(msg)
	}
	return b, nil
}

func (b browser) apply(r Result) (tea.Model, tea.Cmd) {
	b.status, b.statErr = Clean(r.Status), false
	if r.Err != nil {
		b.status, b.statErr = Clean(r.Err.Error()), true
	}
	var cmds []tea.Cmd
	if r.Text != "" {
		text := strings.ReplaceAll(strings.TrimRight(keepColors(r.Text), "\n"), "\t", "    ")
		b.text, b.textTtl, b.scroll, b.mode = strings.Split(text, "\n"), Clean(r.TextTitle), 0, modeText
	}
	if r.Goto > 0 && r.Goto <= len(b.tabs) {
		b.cur = r.Goto - 1
		t := &b.tabs[b.cur]
		t.filter, t.only, t.onlyLabel = r.Filter, r.Only, r.OnlyLabel
		t.cursor, t.offset, t.key = 0, 0, "" // the top row, not the one picked there before
		b.follow(b.cur)
		cmds = append(cmds, b.load(b.cur))
	}
	if r.Reload {
		cmds = append(cmds, b.load(b.cur))
	}
	if r.Exec != nil {
		cmds = append(cmds, tea.ExecProcess(r.Exec, func(err error) tea.Msg { return execMsg{err} }))
	}
	if r.Quit {
		return b, tea.Quit
	}
	return b, tea.Batch(cmds...)
}

// what names a row in prompts: its Name, or else its label.
func what(it Item) string {
	if it.Name != "" {
		return Clean(it.Name)
	}
	r := []rune(strings.Join(strings.Fields(Clean(plain(it.Label))), " "))
	if len(r) > 60 {
		return string(r[:59]) + "…"
	}
	return string(r)
}

func (b browser) run(view int, it Item, a Action) (tea.Model, tea.Cmd) {
	b.busy, b.status = a.Label+" "+what(it)+"…", ""
	key, fn := a.Key, b.o.Action
	return b, func() tea.Msg { return actionMsg(fn(view, it, key)) }
}

func (b browser) keyList(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	t := &b.tabs[b.cur]
	if p := b.confirm; p != nil { // y runs it; any other key cancels
		b.confirm = nil
		if k.String() != "y" && k.String() != "Y" {
			b.status, b.statErr = "cancelled", false
			return b, nil
		}
		if b.busy != "" { // one action at a time: their results would mix
			b.status, b.statErr = "not run: "+strings.TrimSuffix(b.busy, "…")+" is still running", true
			return b, nil
		}
		return b.run(p.view, p.it, p.act)
	}
	switch k.String() {
	case "ctrl+c", "q":
		return b, tea.Quit
	case "esc":
		switch {
		case t.filter != "":
			t.filter, t.cursor, t.offset = "", 0, 0
		case t.only != nil:
			t.only, t.onlyLabel, t.cursor, t.offset = nil, "", 0, 0
		default:
			return b, tea.Quit
		}
		b.follow(b.cur)
		return b, nil
	case "/":
		b.mode = modeFilter
		b.input.SetValue(t.filter)
		b.input.CursorEnd()
		return b, b.input.Focus()
	case "tab", "right":
		return b.switchTab((b.cur + 1) % len(b.tabs))
	case "shift+tab", "left":
		return b.switchTab((b.cur + len(b.tabs) - 1) % len(b.tabs))
	case "R", "ctrl+r":
		if b.o.Reload != nil {
			b.o.Reload()
		}
		return b, b.load(b.cur)
	case "up", "k":
		b.move(-1)
		return b, nil
	case "down", "j":
		b.move(1)
		return b, nil
	case "pgup", "ctrl+u":
		b.move(-b.listHeight())
		return b, nil
	case "pgdown", "ctrl+f":
		b.move(b.listHeight())
		return b, nil
	case "g", "home":
		b.move(-1 << 30)
		return b, nil
	case "G", "end":
		b.move(1 << 30)
		return b, nil
	}
	if s := k.String(); len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
		if i := int(s[0] - '1'); i < len(b.tabs) {
			return b.switchTab(i)
		}
	}
	// an action of this tab
	for _, a := range b.o.Views[b.cur].Actions {
		if a.Key != k.String() {
			continue
		}
		it, ok := b.selected()
		if !ok || b.busy != "" || it.Key == "" {
			return b, nil
		}
		if a.Confirm {
			b.confirm = &pending{b.cur, it, a}
			return b, nil
		}
		return b.run(b.cur, it, a)
	}
	return b, nil
}

func (b browser) switchTab(i int) (tea.Model, tea.Cmd) {
	b.cur = i
	t := &b.tabs[i]
	b.status = ""
	if t.loading {
		return b, nil
	}
	if t.stale || t.loadedAt.IsZero() || time.Since(t.loadedAt) > b.o.Views[i].Every {
		return b, b.load(i)
	}
	return b, nil
}

func (b browser) keyFilter(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	t := &b.tabs[b.cur]
	switch k.String() {
	case "enter", "up", "down":
		b.mode = modeList
		b.input.Blur()
		return b, nil
	case "esc":
		b.mode = modeList
		t.filter = ""
		b.input.Blur()
		b.follow(b.cur)
		return b, nil
	case "ctrl+c":
		return b, tea.Quit
	}
	var cmd tea.Cmd
	b.input, cmd = b.input.Update(k)
	if v := b.input.Value(); v != t.filter {
		t.filter, t.cursor, t.offset = v, 0, 0
		b.follow(b.cur)
	}
	return b, cmd
}

func (b browser) keyText(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	h := b.listHeight() + 1
	switch k.String() {
	case "q", "esc":
		b.mode = modeList
		return b, nil
	case "ctrl+c":
		return b, tea.Quit
	case "up", "k":
		b.scroll--
	case "down", "j", "enter":
		b.scroll++
	case "pgup", "ctrl+u", "b":
		b.scroll -= h
	case "pgdown", "ctrl+f", " ":
		b.scroll += h
	case "g", "home":
		b.scroll = 0
	case "G", "end":
		b.scroll = len(b.text)
	}
	b.scroll = max(0, min(b.scroll, len(b.text)-h))
	return b, nil
}

// shown: the rows of the tab that pass its Only and its filter (every word,
// any case).
func (b browser) shown(tab int) []Item {
	t := b.tabs[tab]
	words := strings.Fields(strings.ToLower(t.filter))
	if len(words) == 0 && t.only == nil {
		return t.items
	}
	var out []Item
	for _, it := range t.items {
		if t.only != nil && !t.only(it) {
			continue
		}
		text := strings.ToLower(plain(it.Label) + " " + it.Hint + " " + it.Badge)
		ok := true
		for _, w := range words {
			if !strings.Contains(text, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, it)
		}
	}
	return out
}

func (b browser) selected() (Item, bool) {
	rows := b.shown(b.cur)
	t := b.tabs[b.cur]
	if t.cursor < 0 || t.cursor >= len(rows) {
		return Item{}, false
	}
	return rows[t.cursor], true
}

// follow keeps the cursor on the same row (by Key) after the rows change.
func (b *browser) follow(tab int) {
	t := &b.tabs[tab]
	rows := b.shown(tab)
	if t.key != "" {
		for i, it := range rows {
			if it.Key == t.key {
				t.cursor = i
				break
			}
		}
	}
	t.cursor = max(0, min(t.cursor, len(rows)-1))
	if len(rows) > 0 {
		t.key = rows[t.cursor].Key
	}
	b.clampOffset(tab)
}

func (b *browser) move(d int) {
	t := &b.tabs[b.cur]
	rows := b.shown(b.cur)
	if len(rows) == 0 {
		return
	}
	c := t.cursor + d
	if d < -len(rows) {
		c = 0
	}
	if d > len(rows) {
		c = len(rows) - 1
	}
	t.cursor = max(0, min(c, len(rows)-1))
	t.key, t.moved = rows[t.cursor].Key, true
	b.clampOffset(b.cur)
}

func (b *browser) clampOffset(tab int) {
	t := &b.tabs[tab]
	h := b.listHeight()
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+h {
		t.offset = t.cursor - h + 1
	}
	t.offset = max(0, t.offset)
}

func (b browser) listHeight() int { return max(3, b.h-5) } // tabs, filter/help, header, status

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// plain drops the colors a caller put in a label.
func plain(s string) string { return ansiRe.ReplaceAllString(s, "") }

// keepColors is Clean that keeps colors (SGR sequences): labels and texts
// are colored by the caller around text that came from a cluster.
func keepColors(s string) string {
	if !needsClean(s) {
		return s
	}
	var out strings.Builder
	last := 0
	for _, m := range ansiRe.FindAllStringIndex(s, -1) {
		out.WriteString(Clean(s[last:m[0]]))
		out.WriteString(s[m[0]:m[1]])
		last = m[1]
	}
	out.WriteString(Clean(s[last:]))
	return out.String()
}

// ── view ───────────────────────────────────────────────────────────────

var tabOn = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("25")).Bold(true)

func (b browser) View() string {
	var s strings.Builder
	t := b.tabs[b.cur]

	// tabs
	line := bold.Render(b.o.Title) + "  "
	for i, v := range b.o.Views {
		label := fmt.Sprintf(" %d %s ", i+1, v.Name)
		if i == b.cur {
			line += tabOn.Render(label)
		} else {
			line += dim.Render(label)
		}
	}
	switch {
	case t.loading && !t.ready:
		line += "  " + b.spin.View()
	case t.loading:
		line += "  " + b.spin.View() + dim.Render(fmt.Sprintf(" %d rows · refreshing", len(b.shown(b.cur))))
	case t.ready:
		line += dim.Render(fmt.Sprintf("  %d rows · %s", len(b.shown(b.cur)), t.loadedAt.Format("15:04:05")))
	}
	s.WriteString(lipgloss.NewStyle().MaxWidth(b.w).Render(line) + "\n")

	if b.mode == modeText {
		return s.String() + b.viewText()
	}

	// filter or help
	var scope []string
	if t.onlyLabel != "" {
		scope = append(scope, t.onlyLabel)
	}
	if t.filter != "" {
		scope = append(scope, "/ "+t.filter)
	}
	switch {
	case b.mode == modeFilter:
		s.WriteString(b.input.View() + "\n")
	case len(scope) > 0:
		s.WriteString(accent.Render(strings.Join(scope, " · ")) + dim.Render("   esc clears") + "\n")
	default:
		s.WriteString(lipgloss.NewStyle().MaxWidth(b.w).Render(dim.Render(b.help())) + "\n")
	}

	rows := b.shown(b.cur)
	header := ""
	if h := b.o.Views[b.cur].Header; h != nil {
		header = h()
	}
	s.WriteString(lipgloss.NewStyle().MaxWidth(b.w).Render(dim.Render("   "+b.badgePad()+header)) + "\n")
	h := b.listHeight()
	var body []string
	switch {
	case !t.ready:
		body = []string{"  " + b.spin.View() + " loading…"}
	case t.err != nil && len(t.items) == 0:
		body = []string{"  " + errStyle.Render(Clean(t.err.Error()))}
	case len(rows) == 0:
		body = []string{"  " + dim.Render("(nothing here)")}
	default:
		for i := t.offset; i < len(rows) && i < t.offset+h; i++ {
			body = append(body, b.row(rows[i], i == t.cursor))
		}
	}
	for len(body) < h {
		body = append(body, "")
	}
	s.WriteString(strings.Join(body, "\n") + "\n")

	// status
	switch {
	case b.confirm != nil:
		s.WriteString(Warn.Bold(true).Render(b.confirmText()) + dim.Render("  y to confirm, any other key cancels"))
	case b.busy != "":
		s.WriteString(b.spin.View() + " " + dim.Render(b.busy))
	case b.status != "" && b.statErr:
		s.WriteString(errStyle.Render(lipgloss.NewStyle().MaxWidth(b.w).Render(b.status)))
	case b.status != "":
		s.WriteString(Ok.Render(lipgloss.NewStyle().MaxWidth(b.w).Render(b.status)))
	case t.err != nil:
		s.WriteString(errStyle.Render(lipgloss.NewStyle().MaxWidth(b.w).Render("refresh failed: " + Clean(t.err.Error()))))
	}
	return s.String()
}

// confirmText is the question before a Confirm action: "delete pod
// default/web-7d9f on use (PROD)?".
func (b browser) confirmText() string {
	if b.confirm == nil {
		return ""
	}
	return b.confirm.act.Label + " " + what(b.confirm.it) + "?"
}

func (b browser) help() string {
	var parts []string
	for _, a := range b.o.Views[b.cur].Actions {
		k := a.Key
		if k == "enter" {
			k = "⏎"
		}
		parts = append(parts, k+" "+a.Label)
	}
	parts = append(parts, "/ filter", "tab/1-9 view", "R reload", "q quit")
	return strings.Join(parts, " · ")
}

func (b browser) badgePad() string {
	for _, it := range b.tabs[b.cur].items {
		if it.Badge != "" {
			return "     "
		}
	}
	return ""
}

func (b browser) row(it Item, selected bool) string {
	cur := "   "
	if selected {
		cur = selRow.Render(" > ")
	}
	tag := ""
	switch it.Badge {
	case "":
		tag = b.badgePad()
	case "PROD":
		tag = prodTag.Render("PROD") + " "
	case "FAIL":
		tag = errStyle.Bold(true).Render("FAIL") + " "
	case "SUSP", "WAIT":
		tag = Warn.Render(padRight(it.Badge, 4)) + " "
	default:
		tag = devTag.Render(padRight(it.Badge, 4)) + " "
	}
	label := keepColors(it.Label)
	switch {
	case selected:
		label = selRow.Render(plain(label)) // one style for the whole row
	case it.Dim:
		label = dim.Render(plain(label))
	}
	row := cur + tag + label
	if it.Hint != "" {
		row += "  " + dim.Render(Clean(it.Hint))
	}
	return lipgloss.NewStyle().MaxWidth(b.w).Render(row)
}

func (b browser) viewText() string {
	h := b.listHeight() + 1
	var s strings.Builder
	s.WriteString(bold.Render(b.textTtl) + dim.Render(fmt.Sprintf("   %d lines · ↑↓ pgup/pgdn g/G scroll · q back", len(b.text))) + "\n")
	end := min(len(b.text), b.scroll+h)
	for i := b.scroll; i < end; i++ {
		s.WriteString(lipgloss.NewStyle().MaxWidth(b.w).Render(b.text[i]) + "\n")
	}
	for i := end - b.scroll; i < h; i++ {
		s.WriteString("\n")
	}
	return s.String()
}
