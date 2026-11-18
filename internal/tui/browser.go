package tui

import (
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"sort"
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
// filters), and text results (describe, YAML…) open in a viewer. Space marks
// rows for the actions that take many; ":" opens a tab of another kind
// (BrowserOptions.Command); shift+letters sort; a Goto can be undone with
// esc or backspace. Rows a refresh adds or changes light up for a few
// seconds.

// Action is a key that does something with the selected row.
type Action struct {
	Key     string // as Bubble Tea names it: "l", "ctrl+d", "enter"
	Label   string // shown in the help line
	Confirm bool   // changes something: asks y/N first (production still asks again)
	// Multi: with rows marked (space), it acts on every marked row at once
	// through BrowserOptions.ActionMany, after one y/N that lists them.
	Multi bool
}

// SortKey is a key that sorts a tab: "N" by name. Pressed again, it
// reverses the order.
type SortKey struct {
	Key  string
	Name string              // shown in the header: "sort: restarts ↓"
	Cmp  func(a, b Item) int // ascending; rows without a Key always come last
	Desc bool                // the first press sorts descending (restarts, CPU: the most first)
}

// Choice is a short list to pick from (which container?). Then runs in the
// background with the index picked, like an action.
type Choice struct {
	Title   string
	Options []string
	Then    func(i int) Result
}

// View is one tab.
type View struct {
	Name   string
	Header func() string // column titles, aligned with the rows' Label (after a load)
	// Load runs in the background and returns the tab's rows. While it runs
	// it may call partial with the rows so far (all of them, not only the
	// new ones): a view of many clusters shows each one as it answers.
	Load  func(partial func([]Item)) ([]Item, error)
	Every time.Duration // refresh interval (0: only on R)
	// Wake, when set, says the rows changed (a watch): the tab loads again
	// soon after each send (sends close together load once), or, hidden,
	// when it is shown. Closing it stops that.
	Wake <-chan struct{}
	// Live, when set, is what the status line says of a tab with Wake
	// instead of "live" ("live · polling sa": not every cluster is
	// watched); "" shows the time of the last load, as without Wake.
	Live    func() string
	Actions []Action
	Sorts   []SortKey
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
	// Open adds a tab (or replaces the one an earlier Open added) and goes
	// there, with Filter/Only like Goto: ":ingress".
	Open *View
	// Choose asks which one first (a container); its Then is the result.
	Choose *Choice
	// Run runs in this process with the terminal (the browser steps aside),
	// then the browser comes back and reloads: a change that confirms on the
	// terminal first.
	Run func() error
}

// BrowserOptions configures a browser.
type BrowserOptions struct {
	Title  string
	Views  []View
	Start  int
	Filter string                                     // the starting tab's filter
	Action func(view int, it Item, key string) Result // runs in the background
	// ActionMany runs a Multi action on the marked rows (in the background).
	ActionMany func(view int, its []Item, key string) Result
	// Command runs what was typed after ":" (in the background); its
	// Result usually Opens a tab or Goes to one.
	Command func(text string) Result
	// Complete suggests what to type after ":" (it must not block), best
	// first: each one is shown whole, and its first word is what gets typed.
	Complete func(prefix string) []string
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
	items     []Item // sorted by the tab's sort, when it has one
	raw       []Item // as loaded: the order without a sort
	err       error
	ready     bool      // a load answered (in part, at least): rows or an error to show
	loading   bool      //
	again     bool      // asked to load while loading: load once more when it ends
	stale     bool      // its refresh came while hidden: load when it is shown
	waking    bool      // waits on View.Wake
	loadStart time.Time // when the running load started
	loadedAt  time.Time // when the last load ended
	gen       int
	filter    string
	only      func(Item) bool // Result.Only
	onlyLabel string
	cursor    int
	offset    int
	key       string // the selected row's Key, kept across refreshes
	moved     bool   // the cursor was moved; until then a first load's partial rows keep it on top
	marks     map[string]bool
	sort      int // 1+index in View.Sorts; 0: as loaded
	desc      bool
	// seeded: a load ended well; from then on, the rows a load adds or
	// changes (Item.Sig) light up for a while (lit, by Key).
	seeded bool
	lit    map[string]litRow
	hides  bool // some rows are Hidden
}

// litRow is a row a refresh brought: since when, and whether it is new or
// only changed.
type litRow struct {
	at    time.Time
	isNew bool
}

// How long a row a refresh added or changed stays highlighted: strong at
// first, then only its marker, then not at all. While any is lit the
// browser redraws every fadeStep.
const (
	litStrong = 3 * time.Second
	litFor    = 5 * time.Second
	fadeStep  = time.Second
)

// What the keys do.
const (
	modeList    = iota // move, act on the selected row
	modeFilter         // typing the filter
	modeText           // reading an action's text
	modeCommand        // typing after ":"
	modeChoose         // picking one of a Choice
)

// navEntry is where a Goto came from: esc or backspace goes back there.
type navEntry struct {
	tab       string // its View.Name (an opened tab may be gone)
	filter    string
	only      func(Item) bool
	onlyLabel string
	key       string
}

type browser struct {
	o       BrowserOptions
	fixed   int // the tabs BrowserOptions gave; an Open adds one after them
	tabs    []tabState
	cur     int
	mode    int // modeList, modeFilter…
	input   textinput.Model
	cmdIn   textinput.Model
	sugSel  int // the highlighted suggestion after ":"
	spin    spinner.Model
	busy    string // an action is running
	status  string
	statErr bool
	confirm *pending // a Confirm action waiting for y
	text    []string
	textTtl string
	scroll  int
	choice  *Choice
	choiceI int
	history []navEntry
	gens    *int // load generations, unique across tabs (an opened tab reuses an index)
	fading  bool // a fadeMsg is on its way
	now     func() time.Time
	w, h    int
}

type pending struct {
	view int
	its  []Item // one, or the marked rows of a Multi action
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
	fadeMsg    struct{}
	wakeMsg    struct {
		tab int
		ch  <-chan struct{}
	}
	actionMsg Result
	execMsg   struct{ err error }
)

func newBrowser(o BrowserOptions) browser {
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.PromptStyle = accent
	ci := textinput.New()
	ci.Prompt = ": "
	ci.PromptStyle = accent
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = accent
	o.Views = append([]View(nil), o.Views...) // an Open appends: never into the caller's array
	b := browser{o: o, fixed: len(o.Views), tabs: make([]tabState, len(o.Views)), cur: o.Start, input: ti, cmdIn: ci, spin: sp,
		gens: new(int), now: time.Now, w: 100, h: 30}
	for i := range b.tabs {
		b.tabs[i].marks = map[string]bool{}
	}
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
	t.gen = max(*b.gens, t.gen) + 1
	*b.gens = t.gen
	t.loading, t.stale, t.again, t.loadStart = true, false, false, time.Now()
	gen, fn := t.gen, b.o.Views[tab].Load
	s := newLoadStream()
	run := func() tea.Msg {
		items, err := fn(s.partial)
		s.done <- loadedMsg{tab, gen, items, err}
		return nil
	}
	return tea.Batch(run, s.next(tab, gen))
}

// wakeDelay gathers the changes a View.Wake reports close together into one
// load.
const wakeDelay = 500 * time.Millisecond

// wakeOn waits for the next send on a View.Wake.
func wakeOn(tab int, ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return nil
		}
		time.Sleep(wakeDelay)
		return wakeMsg{tab, ch}
	}
}

// setItems takes a load's rows, sorted by the tab's sort. Once a load is
// complete, marks of rows that are gone are dropped.
func (b *browser) setItems(tab int, items []Item, final bool) {
	t := &b.tabs[tab]
	if t.seeded {
		b.light(tab, t.raw, items)
	}
	t.raw = items
	t.items = b.sorted(tab, items)
	t.hides = slices.ContainsFunc(items, func(it Item) bool { return it.Hidden })
	if final {
		t.seeded = true
	}
	if final && len(t.marks) > 0 {
		keep := map[string]bool{}
		for _, it := range items {
			if t.marks[it.Key] {
				keep[it.Key] = true
			}
		}
		t.marks = keep
	}
}

// light marks the rows of items that old did not have, or had with another
// Sig. By Key: rows that only moved (a sort, a neighbour gone) stay as they
// are, and rows that went take their light with them.
func (b *browser) light(tab int, old, items []Item) {
	t := &b.tabs[tab]
	sigs := make(map[string]string, len(old))
	for _, it := range old {
		if it.Key != "" {
			sigs[it.Key] = it.Sig
		}
	}
	lit := make(map[string]litRow, len(t.lit))
	now := b.now()
	for _, it := range items {
		if it.Key == "" {
			continue
		}
		sig, seen := sigs[it.Key]
		switch {
		case !seen:
			lit[it.Key] = litRow{now, true}
		case sig != it.Sig:
			lit[it.Key] = litRow{now, false}
		default:
			if l, ok := t.lit[it.Key]; ok && now.Sub(l.at) < litFor {
				lit[it.Key] = l
			}
		}
	}
	t.lit = lit
}

// litOf: how the row is highlighted now — on, strong, and whether it is new.
func (b browser) litOf(it Item) (on, strong, isNew bool) {
	l, ok := b.tabs[b.cur].lit[it.Key]
	if !ok || it.Key == "" {
		return false, false, false
	}
	age := b.now().Sub(l.at)
	return age < litFor, age < litStrong, l.isNew
}

// fade drops the lights that are out and, while some are left, asks for
// the next fadeMsg (one at a time): the rows dim and go back to normal
// without a key or a refresh to redraw them.
func (b *browser) fade() tea.Cmd {
	now, left := b.now(), false
	for i := range b.tabs {
		for k, l := range b.tabs[i].lit {
			if now.Sub(l.at) >= litFor {
				delete(b.tabs[i].lit, k)
			} else {
				left = true
			}
		}
	}
	if !left || b.fading {
		return nil
	}
	b.fading = true
	return tea.Tick(fadeStep, func(time.Time) tea.Msg { return fadeMsg{} })
}

// sorted is items in the tab's sort order (a copy); rows without a Key
// (skipped clusters) stay last.
func (b browser) sorted(tab int, items []Item) []Item {
	t := b.tabs[tab]
	sorts := b.o.Views[tab].Sorts
	if t.sort == 0 || t.sort > len(sorts) {
		return items
	}
	cmp := sorts[t.sort-1].Cmp
	out := append([]Item(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		a, c := out[i], out[j]
		if (a.Key == "") != (c.Key == "") {
			return c.Key == ""
		}
		if a.Key == "" {
			return false
		}
		if t.desc {
			return cmp(c, a) < 0
		}
		return cmp(a, c) < 0
	})
	return out
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
		if msg.tab >= len(b.tabs) {
			return b, nil
		}
		t := &b.tabs[msg.tab]
		if msg.gen != t.gen || !t.loading {
			return b, nil
		}
		b.setItems(msg.tab, msg.items, false)
		t.err, t.ready = nil, true
		if t.loadedAt.IsZero() && !t.moved { // first rows still coming in sorted: stay on top
			t.key, t.cursor = "", 0
		}
		b.follow(msg.tab)
		return b, tea.Batch(msg.s.next(msg.tab, msg.gen), b.fade())

	case loadedMsg:
		if msg.tab >= len(b.tabs) {
			return b, nil
		}
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
			b.setItems(msg.tab, msg.items, msg.err == nil)
			t.err = msg.err
		}
		b.follow(msg.tab)
		wake := b.fade()
		if ch := b.o.Views[msg.tab].Wake; ch != nil && !t.waking {
			t.waking = true
			wake = tea.Batch(wake, wakeOn(msg.tab, ch))
		}
		if t.again {
			return b, tea.Batch(wake, b.load(msg.tab))
		}
		if every := b.o.Views[msg.tab].Every; every > 0 {
			tab, gen := msg.tab, t.gen
			return b, tea.Batch(wake, tea.Tick(every, func(time.Time) tea.Msg { return refreshMsg{tab, gen} }))
		}
		return b, wake

	case wakeMsg:
		if msg.tab >= len(b.tabs) || b.o.Views[msg.tab].Wake != msg.ch {
			return b, nil // the tab was replaced
		}
		rearm := wakeOn(msg.tab, msg.ch)
		if msg.tab != b.cur {
			b.tabs[msg.tab].stale = true
			return b, rearm
		}
		return b, tea.Batch(rearm, b.load(msg.tab))

	case refreshMsg:
		if msg.tab >= len(b.tabs) {
			return b, nil
		}
		t := &b.tabs[msg.tab]
		if msg.gen != t.gen || t.loading {
			return b, nil
		}
		if msg.tab != b.cur { // loads when shown again; its rows stay meanwhile
			t.stale = true
			return b, nil
		}
		return b, b.load(msg.tab)

	case fadeMsg:
		b.fading = false
		return b, b.fade()

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
		case modeCommand:
			return b.keyCommand(msg)
		case modeChoose:
			return b.keyChoose(msg)
		}
		return b.keyList(msg)
	}
	return b, nil
}

// funcExec runs Result.Run as a tea.ExecCommand: with the terminal.
type funcExec struct{ fn func() error }

func (f funcExec) Run() error        { return f.fn() }
func (funcExec) SetStdin(io.Reader)  {}
func (funcExec) SetStdout(io.Writer) {}
func (funcExec) SetStderr(io.Writer) {}

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
	if r.Choose != nil && len(r.Choose.Options) > 0 {
		b.choice, b.choiceI, b.mode = r.Choose, 0, modeChoose
	}
	switch {
	case r.Open != nil:
		b.push()
		b.openTab(*r.Open)
		r.Goto = len(b.tabs)
	case r.Goto > 0 && r.Goto <= len(b.tabs) && (r.Goto-1 != b.cur || r.Only != nil || r.Filter != ""):
		b.push()
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
	if r.Run != nil {
		cmds = append(cmds, tea.Exec(funcExec{r.Run}, func(err error) tea.Msg { return execMsg{err} }))
	}
	if r.Quit {
		return b, tea.Quit
	}
	return b, tea.Batch(cmds...)
}

// live is what the status line says of a tab a watch keeps up to date
// ("" for one that refreshes).
func (b browser) live(tab int) string {
	v := b.o.Views[tab]
	switch {
	case v.Wake == nil:
		return ""
	case v.Live != nil:
		return v.Live()
	}
	return "live"
}

// openTab puts v after the fixed tabs, replacing a tab opened before.
func (b *browser) openTab(v View) {
	b.o.Views, b.tabs = b.o.Views[:b.fixed:b.fixed], b.tabs[:b.fixed:b.fixed]
	b.o.Views = append(b.o.Views, v)
	b.tabs = append(b.tabs, tabState{marks: map[string]bool{}})
}

// closeOpened removes the opened tab; the cursor goes back to the fixed tab
// it was opened from.
func (b *browser) closeOpened() {
	if len(b.o.Views) <= b.fixed {
		return
	}
	b.o.Views, b.tabs = b.o.Views[:b.fixed:b.fixed], b.tabs[:b.fixed:b.fixed]
	if b.cur >= b.fixed {
		b.cur = 0
		for i := len(b.history) - 1; i >= 0; i-- {
			if j := b.tabIndex(b.history[i].tab); j >= 0 {
				b.cur = j
				break
			}
		}
	}
}

func (b browser) tabIndex(name string) int {
	for i, v := range b.o.Views {
		if v.Name == name {
			return i
		}
	}
	return -1
}

// push remembers where the cursor is, for back.
func (b *browser) push() {
	t := b.tabs[b.cur]
	b.history = append(b.history, navEntry{tab: b.o.Views[b.cur].Name, filter: t.filter, only: t.only, onlyLabel: t.onlyLabel, key: t.key})
	if len(b.history) > 30 {
		b.history = b.history[len(b.history)-30:]
	}
}

// back returns to where the last Goto came from; the tab left loses the
// filter the Goto gave it. false: nowhere to go back to.
func (b *browser) back() (tea.Cmd, bool) {
	for len(b.history) > 0 {
		e := b.history[len(b.history)-1]
		b.history = b.history[:len(b.history)-1]
		i := b.tabIndex(e.tab)
		if i < 0 {
			continue
		}
		left := &b.tabs[b.cur]
		left.filter, left.only, left.onlyLabel = "", nil, ""
		b.follow(b.cur)
		t := &b.tabs[i]
		t.filter, t.only, t.onlyLabel, t.key = e.filter, e.only, e.onlyLabel, e.key
		b.follow(i)
		if b.cur >= b.fixed && i < b.fixed { // back from an opened tab: it closes
			b.o.Views, b.tabs = b.o.Views[:b.fixed:b.fixed], b.tabs[:b.fixed:b.fixed]
		}
		m, cmd := b.switchTab(i)
		*b = m.(browser)
		return cmd, true
	}
	return nil, false
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

func (b browser) run(view int, its []Item, a Action) (tea.Model, tea.Cmd) {
	key := a.Key
	if len(its) == 1 {
		b.busy, b.status = a.Label+" "+what(its[0])+"…", ""
		it, fn := its[0], b.o.Action
		return b, func() tea.Msg { return actionMsg(fn(view, it, key)) }
	}
	b.busy, b.status = fmt.Sprintf("%s %d objects…", a.Label, len(its)), ""
	b.tabs[view].marks = map[string]bool{} // done with them, whatever the answer
	fn := b.o.ActionMany
	return b, func() tea.Msg { return actionMsg(fn(view, its, key)) }
}

// marked: the tab's marked rows, in its order.
func (b browser) marked(tab int) []Item {
	t := b.tabs[tab]
	if len(t.marks) == 0 {
		return nil
	}
	var out []Item
	for _, it := range t.items {
		if t.marks[it.Key] {
			out = append(out, it)
		}
	}
	return out
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
		return b.run(p.view, p.its, p.act)
	}
	switch k.String() {
	case "ctrl+c", "q":
		return b, tea.Quit
	case "esc":
		switch {
		case t.filter != "":
			t.filter, t.cursor, t.offset = "", 0, 0
		case len(t.marks) > 0:
			t.marks = map[string]bool{}
		default:
			if cmd, ok := b.back(); ok {
				return b, cmd
			}
			switch {
			case t.only != nil:
				t.only, t.onlyLabel, t.cursor, t.offset = nil, "", 0, 0
			case b.cur >= b.fixed:
				b.closeOpened()
				return b.switchTab(b.cur)
			default:
				return b, tea.Quit
			}
		}
		b.follow(b.cur)
		return b, nil
	case "backspace":
		if cmd, ok := b.back(); ok {
			return b, cmd
		}
		if b.cur >= b.fixed {
			b.closeOpened()
			return b.switchTab(b.cur)
		}
		return b, nil
	case "/":
		b.mode = modeFilter
		b.input.SetValue(t.filter)
		b.input.CursorEnd()
		return b, b.input.Focus()
	case ":":
		if b.o.Command == nil {
			return b, nil
		}
		b.mode = modeCommand
		b.cmdIn.SetValue("")
		b.sugSel = 0
		return b, b.cmdIn.Focus()
	case "?":
		b.text, b.textTtl, b.scroll, b.mode = strings.Split(b.keysHelp(), "\n"), "keys", 0, modeText
		return b, nil
	case " ":
		if it, ok := b.selected(); ok && it.Key != "" {
			if t.marks[it.Key] {
				delete(t.marks, it.Key)
			} else {
				t.marks[it.Key] = true
			}
			b.move(1)
		}
		return b, nil
	case "ctrl+a": // mark every row shown, or unmark them when they all are
		rows := b.shown(b.cur)
		all := true
		for _, it := range rows {
			all = all && (it.Key == "" || t.marks[it.Key])
		}
		for _, it := range rows {
			switch {
			case it.Key == "":
			case all:
				delete(t.marks, it.Key)
			default:
				t.marks[it.Key] = true
			}
		}
		return b, nil
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
	for i, s := range b.o.Views[b.cur].Sorts {
		if s.Key != k.String() {
			continue
		}
		if t.sort == i+1 {
			t.desc = !t.desc
		} else {
			t.sort, t.desc = i+1, s.Desc
		}
		t.items = b.sorted(b.cur, t.raw)
		t.cursor, t.offset, t.key = 0, 0, "" // the top of the new order
		b.follow(b.cur)
		return b, nil
	}
	// an action of this tab
	for _, a := range b.o.Views[b.cur].Actions {
		if a.Key != k.String() {
			continue
		}
		if b.busy != "" {
			return b, nil
		}
		var its []Item
		if marked := b.marked(b.cur); a.Multi && len(marked) > 0 && b.o.ActionMany != nil {
			its = marked
		} else if it, ok := b.selected(); ok && it.Key != "" {
			its = []Item{it}
		}
		if len(its) == 0 {
			return b, nil
		}
		if a.Confirm {
			b.confirm = &pending{b.cur, its, a}
			return b, nil
		}
		return b.run(b.cur, its, a)
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

// suggestions: what Complete offers for the text after ":".
func (b browser) suggestions() []string {
	if b.o.Complete == nil {
		return nil
	}
	return b.o.Complete(strings.TrimSpace(b.cmdIn.Value()))
}

func (b browser) keyCommand(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		b.mode = modeList
		b.cmdIn.Blur()
		return b, nil
	case "ctrl+c":
		return b, tea.Quit
	case "up", "ctrl+p", "shift+tab":
		if b.sugSel > 0 {
			b.sugSel--
		}
		return b, nil
	case "down", "ctrl+n":
		if b.sugSel < len(b.suggestions())-1 {
			b.sugSel++
		}
		return b, nil
	case "tab":
		if s := b.suggestions(); len(s) > 0 {
			b.cmdIn.SetValue(suggestionValue(s[min(b.sugSel, len(s)-1)]))
			b.cmdIn.CursorEnd()
			b.sugSel = 0
		}
		return b, nil
	case "enter":
		b.mode = modeList
		b.cmdIn.Blur()
		text := strings.TrimSpace(b.cmdIn.Value())
		// a name typed whole stays; otherwise the highlighted suggestion
		if s := b.suggestions(); len(s) > 0 && !slices.ContainsFunc(s, func(x string) bool { return suggestionValue(x) == text }) {
			text = suggestionValue(s[min(b.sugSel, len(s)-1)])
		}
		if text == "" || b.busy != "" {
			return b, nil
		}
		b.busy, b.status = ":"+text+"…", ""
		fn := b.o.Command
		return b, func() tea.Msg { return actionMsg(fn(text)) }
	}
	var cmd tea.Cmd
	before := b.cmdIn.Value()
	b.cmdIn, cmd = b.cmdIn.Update(k)
	if b.cmdIn.Value() != before {
		b.sugSel = 0
	}
	return b, cmd
}

// suggestionValue is what a suggestion types: its first word.
func suggestionValue(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return s
}

// maxSuggestions is how many suggestions show under the ":" prompt.
const maxSuggestions = 8

// suggestionLines: the suggestions shown under the ":" prompt, a window
// around the highlighted one.
func (b browser) suggestionLines() []string {
	sg := b.suggestions()
	if len(sg) == 0 {
		return nil
	}
	sel := min(b.sugSel, len(sg)-1)
	start := max(0, min(sel-maxSuggestions/2, len(sg)-maxSuggestions))
	end := min(len(sg), start+maxSuggestions)
	var out []string
	for i := start; i < end; i++ {
		name, rest, _ := strings.Cut(sg[i], " ")
		line := "  " + name + dim.Render(rest)
		if i == sel {
			line = accent.Render("› "+name) + dim.Render(rest)
		}
		out = append(out, lipgloss.NewStyle().MaxWidth(b.w).Render(line))
	}
	more := ""
	if len(sg) > end || start > 0 {
		more = fmt.Sprintf(" · %d of %d", sel+1, len(sg))
	}
	out = append(out, dim.Render("  ↑↓ choose · tab completes · enter opens"+more))
	return out
}

func (b browser) keyChoose(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	ch := b.choice
	pick := -1
	switch s := k.String(); s {
	case "esc", "q":
		b.mode, b.choice, b.status = modeList, nil, "cancelled"
		return b, nil
	case "ctrl+c":
		return b, tea.Quit
	case "up", "k":
		b.choiceI = max(0, b.choiceI-1)
	case "down", "j", "tab":
		b.choiceI = min(len(ch.Options)-1, b.choiceI+1)
	case "enter":
		pick = b.choiceI
	default:
		if len(s) == 1 && s[0] >= '1' && s[0] <= '9' && int(s[0]-'1') < len(ch.Options) {
			pick = int(s[0] - '1')
		}
	}
	if pick < 0 {
		return b, nil
	}
	b.mode, b.choice = modeList, nil
	b.busy, b.status = Clean(ch.Options[pick])+"…", ""
	return b, func() tea.Msg { return actionMsg(ch.Then(pick)) }
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
// any case). Hidden rows need an Only.
func (b browser) shown(tab int) []Item {
	t := b.tabs[tab]
	words := strings.Fields(strings.ToLower(t.filter))
	if len(words) == 0 && t.only == nil && !t.hides {
		return t.items
	}
	var out []Item
	for _, it := range t.items {
		if t.only != nil && !t.only(it) || t.only == nil && it.Hidden {
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

func (b browser) listHeight() int { // tabs, filter/help, header, status; the ":" suggestions
	n := b.h - 5
	if b.mode == modeCommand {
		n -= len(b.suggestionLines())
	}
	return max(3, n)
}

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

var (
	tabOn     = lipgloss.NewStyle().Foreground(lipgloss.Color("231")).Background(lipgloss.Color("25")).Bold(true)
	markStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	// a row a refresh just added (+) or changed (~): a faint band at first
	litStyle = lipgloss.NewStyle().Bold(true).Background(lipgloss.AdaptiveColor{Light: "254", Dark: "236"})
	litMark  = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
)

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
	marks := ""
	if n := len(t.marks); n > 0 {
		marks = fmt.Sprintf(" · %d marked", n)
	}
	switch {
	case t.loading && !t.ready:
		line += "  " + b.spin.View()
	case t.loading && time.Since(t.loadStart) > 400*time.Millisecond: // a quick one doesn't blink
		line += "  " + b.spin.View() + dim.Render(fmt.Sprintf(" %d rows%s · refreshing", len(b.shown(b.cur)), marks))
	case t.ready && b.live(b.cur) != "":
		line += dim.Render(fmt.Sprintf("  %d rows%s · %s", len(b.shown(b.cur)), marks, Clean(b.live(b.cur))))
	case t.ready:
		line += dim.Render(fmt.Sprintf("  %d rows%s · %s", len(b.shown(b.cur)), marks, t.loadedAt.Format("15:04:05")))
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
	case b.mode == modeCommand:
		s.WriteString(lipgloss.NewStyle().MaxWidth(b.w).Render(b.cmdIn.View()) + "\n")
		for _, l := range b.suggestionLines() {
			s.WriteString(l + "\n")
		}
	case len(scope) > 0:
		back := "   esc clears"
		if len(b.history) > 0 {
			back = "   esc back"
		}
		s.WriteString(accent.Render(strings.Join(scope, " · ")) + dim.Render(back) + "\n")
	default:
		s.WriteString(lipgloss.NewStyle().MaxWidth(b.w).Render(dim.Render(b.help())) + "\n")
	}

	rows := b.shown(b.cur)
	header := ""
	if h := b.o.Views[b.cur].Header; h != nil {
		header = h()
	}
	if sorts := b.o.Views[b.cur].Sorts; t.sort > 0 && t.sort <= len(sorts) {
		arrow := "↑"
		if t.desc {
			arrow = "↓"
		}
		header += "   [sort: " + sorts[t.sort-1].Name + " " + arrow + "]"
	}
	s.WriteString(lipgloss.NewStyle().MaxWidth(b.w).Render(dim.Render("   "+b.badgePad()+header)) + "\n")
	h := b.listHeight()
	var body []string
	switch {
	case b.mode == modeChoose && b.choice != nil:
		body = b.viewChoice()
	case b.confirm != nil && len(b.confirm.its) > 1:
		body = b.viewConfirmList(h)
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
	s.WriteString(strings.Join(body[:h], "\n") + "\n")

	// status
	switch {
	case b.confirm != nil:
		s.WriteString(Warn.Bold(true).Render(b.confirmText()) + dim.Render("  y to confirm, any other key cancels"))
	case b.mode == modeChoose:
		s.WriteString(dim.Render("↑↓ or 1-9, enter picks, esc cancels"))
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

func (b browser) viewChoice() []string {
	out := []string{"  " + bold.Render(Clean(b.choice.Title))}
	for i, o := range b.choice.Options {
		line := fmt.Sprintf("   %d  %s", i+1, Clean(o))
		if i == b.choiceI {
			line = selRow.Render(fmt.Sprintf(" > %d  %s", i+1, Clean(o)))
		}
		out = append(out, line)
	}
	return out
}

// viewConfirmList lists every object a Multi action is about to change.
func (b browser) viewConfirmList(h int) []string {
	p := b.confirm
	out := []string{"  " + Warn.Bold(true).Render(fmt.Sprintf("%s these %d objects?", p.act.Label, len(p.its)))}
	for i, it := range p.its {
		if len(out) == h-1 && i < len(p.its)-1 {
			out = append(out, dim.Render(fmt.Sprintf("   … and %d more", len(p.its)-i)))
			break
		}
		name := what(it)
		if n, ok := strings.CutSuffix(name, "(PROD)"); ok {
			name = n + prodTag.Render("PROD")
		}
		out = append(out, "   "+name)
	}
	return out
}

// confirmText is the question before a Confirm action: "delete pod
// default/web-7d9f on use (PROD)?", or "delete 3 objects (PROD)?".
func (b browser) confirmText() string {
	p := b.confirm
	if p == nil {
		return ""
	}
	if len(p.its) == 1 {
		return p.act.Label + " " + what(p.its[0]) + "?"
	}
	prod := ""
	for _, it := range p.its {
		if strings.HasSuffix(what(it), "(PROD)") {
			prod = " (PROD)"
		}
	}
	return fmt.Sprintf("%s %d objects%s?", p.act.Label, len(p.its), prod)
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
	parts = append(parts, "space mark", "/ filter")
	if b.o.Command != nil {
		parts = append(parts, ": kind")
	}
	parts = append(parts, "? keys", "q quit")
	return strings.Join(parts, " · ")
}

// keysHelp is the "?" page: every key of this tab and of the browser.
func (b browser) keysHelp() string {
	v := b.o.Views[b.cur]
	var s strings.Builder
	s.WriteString(bold.Render("this tab ("+v.Name+")") + "\n\n")
	for _, a := range v.Actions {
		note := ""
		if a.Multi {
			note = "   every marked row"
		}
		if a.Confirm {
			note += "   asks y/N first"
		}
		fmt.Fprintf(&s, "  %-9s %s%s\n", a.Key, a.Label, dim.Render(note))
	}
	for _, so := range v.Sorts {
		fmt.Fprintf(&s, "  %-9s sort by %s (again: reverse)\n", so.Key, so.Name)
	}
	s.WriteString("\n" + bold.Render("everywhere") + "\n\n")
	general := [][2]string{
		{"↑↓ j k", "move · pgup/pgdn · g/G top/bottom"},
		{"space", "mark the row · ctrl+a every row shown (again: none)"},
		{"/", "filter (every word, any column)"},
		{"tab 1-9", "switch tab"},
	}
	if b.o.Command != nil {
		general = append(general, [2]string{":", "open another kind: :ingress, :cm, :secret, :crd… (tab completes)"})
	}
	general = append(general,
		[2]string{"esc ⌫", "back to where enter came from · clear the filter or marks · close a : tab"},
		[2]string{"R", "reload (tries skipped clusters again)"},
		[2]string{"q", "quit"})
	for _, g := range general {
		fmt.Fprintf(&s, "  %-9s %s\n", g[0], g[1])
	}
	return s.String()
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
	marked := it.Key != "" && b.tabs[b.cur].marks[it.Key]
	cur := "   "
	switch {
	case selected && marked:
		cur = selRow.Render(" >") + markStyle.Render("●")
	case selected:
		cur = selRow.Render(" > ")
	case marked:
		cur = "  " + markStyle.Render("●")
	}
	lit, strong, isNew := b.litOf(it)
	if lit && !selected && !marked {
		sign := "~"
		if isNew {
			sign = "+"
		}
		cur = "  " + litMark.Render(sign)
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
	case marked:
		label = markStyle.Render(plain(label))
	case lit && strong:
		label = litStyle.Render(plain(label))
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
