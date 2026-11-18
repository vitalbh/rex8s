package tui

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(b browser, keys ...string) (browser, tea.Cmd) {
	var cmd tea.Cmd
	for _, k := range keys {
		var m tea.Model
		m, cmd = b.Update(key(k))
		b = m.(browser)
	}
	return b, cmd
}

func loaded(b browser, tab int, items ...Item) browser {
	m, _ := b.Update(loadedMsg{tab: tab, gen: b.tabs[tab].gen, items: items})
	return m.(browser)
}

func testBrowser(action func(int, Item, string) Result) browser {
	b := newBrowser(BrowserOptions{Title: "t", Action: action, Views: []View{
		{Name: "pods", Load: noLoad, Actions: []Action{{Key: "l", Label: "logs"}}},
		{Name: "svc", Load: noLoad},
	}})
	b.tabs[0].gen = 1
	return loaded(b, 0, Item{Key: "a", Label: "app/api-1"}, Item{Key: "b", Label: "app/web-1"}, Item{Key: "c", Label: "db/pg-0"})
}

func TestBrowserFilterAndFollow(t *testing.T) {
	b := testBrowser(nil)
	b, _ = press(b, "/", "w", "e", "b", "enter")
	if rows := b.shown(0); len(rows) != 1 || rows[0].Key != "b" {
		t.Fatalf("filter web: %v", rows)
	}
	b, _ = press(b, "esc") // clears the filter, cursor stays on web
	if it, _ := b.selected(); it.Key != "b" {
		t.Fatalf("after esc the cursor is on %q, want b", it.Key)
	}
	// a refresh adds a row above: the cursor follows the row, not the index
	b = loaded(b, 0, Item{Key: "0", Label: "app/aaa"}, Item{Key: "a", Label: "app/api-1"}, Item{Key: "b", Label: "app/web-1"})
	if it, _ := b.selected(); it.Key != "b" {
		t.Fatalf("after a refresh the cursor is on %q, want b", it.Key)
	}
}

func TestBrowserActionAndTabs(t *testing.T) {
	var got string
	b := testBrowser(func(view int, it Item, k string) Result { got = it.Key + ":" + k; return Result{Status: "ok"} })
	b, cmd := press(b, "down", "l")
	if cmd == nil || b.busy == "" {
		t.Fatal("l did not start the action")
	}
	m, _ := b.Update(cmd())
	b = m.(browser)
	if got != "b:l" || b.status != "ok" || b.busy != "" {
		t.Fatalf("action got %q, status %q, busy %q", got, b.status, b.busy)
	}
	if _, cmd := press(b, "x"); cmd != nil {
		t.Fatal("a key that is no action did something")
	}
	b, _ = press(b, "tab")
	if b.cur != 1 {
		t.Fatalf("tab: view %d", b.cur)
	}
	b, _ = press(b, "1")
	if b.cur != 0 {
		t.Fatalf("1: view %d", b.cur)
	}
}

func TestBrowserTextViewer(t *testing.T) {
	b := testBrowser(nil)
	m, _ := b.apply(Result{Text: "a\tb\nc", TextTitle: "describe"})
	b = m.(browser)
	if b.mode != 2 || len(b.text) != 2 || b.text[0] != "a    b" {
		t.Fatalf("viewer: mode %d text %q", b.mode, b.text)
	}
	b, _ = press(b, "q")
	if b.mode != 0 {
		t.Fatal("q did not leave the viewer")
	}
}

func TestBrowserConfirm(t *testing.T) {
	ran := 0
	b := newBrowser(BrowserOptions{Title: "t", Action: func(int, Item, string) Result { ran++; return Result{} }, Views: []View{
		{Name: "w", Load: noLoad, Actions: []Action{{Key: "r", Label: "restart", Confirm: true}}},
	}})
	b.tabs[0].gen = 1
	b = loaded(b, 0, Item{Key: "a", Label: "app/api"})
	b, cmd := press(b, "r")
	if cmd != nil || b.confirm == nil {
		t.Fatal("r ran without asking")
	}
	b, cmd = press(b, "n")
	if cmd != nil || b.confirm != nil || b.status != "cancelled" {
		t.Fatal("n did not cancel")
	}
	b, _ = press(b, "r")
	b, cmd = press(b, "y")
	if cmd == nil {
		t.Fatal("y did not run it")
	}
	cmd()
	if ran != 1 {
		t.Fatalf("ran %d times", ran)
	}
}

func TestBrowserFilterIgnoresColors(t *testing.T) {
	b := testBrowser(nil)
	b = loaded(b, 0, Item{Key: "p", Label: "\x1b[31muse\x1b[0m  app/api-1"}, Item{Key: "q", Label: "euw  app/api-1"})
	b, _ = press(b, "/", "u", "s", "e", " ", "a", "p", "i", "enter")
	if rows := b.shown(0); len(rows) != 1 || rows[0].Key != "p" {
		t.Fatalf("filter over a colored label: %v", rows)
	}
}

func noLoad(func([]Item)) ([]Item, error) { return nil, nil }

// startLoad marks a tab as loading, as load does, without running Load.
func startLoad(b browser, tab int) browser {
	b.tabs[tab].gen++
	b.tabs[tab].loading = true
	return b
}

func TestBrowserHiddenTabRefreshKeepsRows(t *testing.T) {
	b := testBrowser(nil)
	b = startLoad(b, 1)
	b = loaded(b, 1, Item{Key: "s", Label: "app/api"})
	gen := b.tabs[1].gen
	// the svc tab's refresh fires while pods is shown
	m, cmd := b.Update(refreshMsg{tab: 1, gen: gen})
	b = m.(browser)
	if cmd != nil || !b.tabs[1].stale || len(b.tabs[1].items) != 1 {
		t.Fatalf("hidden refresh: cmd %v stale %v items %d", cmd != nil, b.tabs[1].stale, len(b.tabs[1].items))
	}
	b, cmd = press(b, "tab")
	if cmd == nil || !b.tabs[1].loading || b.tabs[1].stale {
		t.Fatal("showing a stale tab did not load it")
	}
	if v := b.View(); strings.Contains(v, "loading…") || !strings.Contains(v, "app/api") {
		t.Fatalf("while it reloads the old rows should stay:\n%s", v)
	}
}

func TestBrowserStaleGenIgnored(t *testing.T) {
	b := testBrowser(nil)
	old := b.tabs[0].gen
	b = startLoad(b, 0)
	m, _ := b.Update(loadedMsg{tab: 0, gen: old, items: []Item{{Key: "x", Label: "old"}}})
	b = m.(browser)
	if len(b.tabs[0].items) != 3 || !b.tabs[0].loading {
		t.Fatalf("an old load's answer was taken: %v", b.tabs[0].items)
	}
	m, _ = b.Update(partialMsg{tab: 0, gen: old, items: []Item{{Key: "x"}}})
	if b = m.(browser); len(b.tabs[0].items) != 3 {
		t.Fatal("an old load's partial rows were taken")
	}
}

func TestBrowserRepeatedReload(t *testing.T) {
	retried := 0
	b := testBrowser(nil)
	b.o.Reload = func() { retried++ }
	b, cmd := press(b, "R")
	if cmd == nil || !b.tabs[0].loading {
		t.Fatal("R did not load")
	}
	gen := b.tabs[0].gen
	b, cmd = press(b, "R", "R")
	if cmd != nil || b.tabs[0].gen != gen || !b.tabs[0].again {
		t.Fatalf("R while loading started another load (gen %d → %d)", gen, b.tabs[0].gen)
	}
	if retried != 3 {
		t.Fatalf("Reload ran %d times, want 3", retried)
	}
	// the running load ends: one more load, not two
	m, cmd := b.Update(loadedMsg{tab: 0, gen: gen})
	b = m.(browser)
	if cmd == nil || b.tabs[0].gen != gen+1 || !b.tabs[0].loading || b.tabs[0].again {
		t.Fatalf("after the load: gen %d loading %v again %v", b.tabs[0].gen, b.tabs[0].loading, b.tabs[0].again)
	}
}

func TestBrowserPartialRows(t *testing.T) {
	b := newBrowser(BrowserOptions{Title: "t", Views: []View{{Name: "pods", Load: noLoad}}})
	b = startLoad(b, 0)
	s := newLoadStream()
	s.partial([]Item{{Key: "a", Label: "use  app/a"}})
	s.partial([]Item{{Key: "a", Label: "use  app/a"}, {Key: "b", Label: "euw  app/b"}}) // only the latest counts
	msg := s.next(0, b.tabs[0].gen)()
	m, cmd := b.Update(msg)
	b = m.(browser)
	if len(b.tabs[0].items) != 2 || !b.tabs[0].ready || !b.tabs[0].loading || cmd == nil {
		t.Fatalf("partial: %d rows, ready %v loading %v", len(b.tabs[0].items), b.tabs[0].ready, b.tabs[0].loading)
	}
	if v := b.View(); strings.Contains(v, "loading…") || !strings.Contains(v, "app/b") {
		t.Fatalf("partial rows not shown:\n%s", v)
	}
	s.done <- loadedMsg{tab: 0, gen: b.tabs[0].gen, items: []Item{{Key: "b", Label: "euw  app/b"}}}
	m, _ = b.Update(cmd())
	if b = m.(browser); b.tabs[0].loading || len(b.tabs[0].items) != 1 {
		t.Fatal("the load's end did not replace the partial rows")
	}
}

func TestBrowserConfirmWhileBusy(t *testing.T) {
	ran := 0
	b := newBrowser(BrowserOptions{Title: "t", Action: func(int, Item, string) Result { ran++; return Result{} }, Views: []View{
		{Name: "w", Load: noLoad, Actions: []Action{{Key: "r", Label: "restart", Confirm: true}}},
	}})
	b.tabs[0].gen = 1
	b = loaded(b, 0, Item{Key: "a", Label: "app/api"})
	b, _ = press(b, "r")
	b.busy = "logs app/api…" // another action started meanwhile
	b, cmd := press(b, "y")
	if cmd != nil || ran != 0 || b.confirm != nil || !b.statErr || !strings.Contains(b.status, "still running") {
		t.Fatalf("y while busy: cmd %v ran %d status %q", cmd != nil, ran, b.status)
	}
}

func TestBrowserConfirmNamesTheObject(t *testing.T) {
	b := newBrowser(BrowserOptions{Title: "t", Views: []View{
		{Name: "pods", Load: noLoad, Actions: []Action{{Key: "ctrl+d", Label: "delete", Confirm: true}}},
	}})
	b.tabs[0].gen = 1
	// -A: the label starts with a long cluster name in red
	b = loaded(b, 0, Item{Key: "p", Label: "\x1b[31mproduction-eu-west\x1b[0m  default/web-7d9f  1/1 Running",
		Name: "pod default/web-7d9f on production-eu-west (PROD)"})
	m, _ := b.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	b = m.(browser)
	if got, want := b.confirmText(), "delete pod default/web-7d9f on production-eu-west (PROD)?"; got != want {
		t.Fatalf("confirm %q, want %q", got, want)
	}
	if !strings.Contains(b.View(), "delete pod default/web-7d9f on production-eu-west (PROD)?") {
		t.Fatal("the prompt is not on screen")
	}
	// without a Name: the whole label, not only its first field
	if got := what(Item{Label: "euw  deploy  app/web  3/3"}); got != "euw deploy app/web 3/3" {
		t.Fatalf("what = %q", got)
	}
}

func TestBrowserGotoResetsSelection(t *testing.T) {
	b := testBrowser(nil)
	b = startLoad(b, 1)
	b = loaded(b, 1, Item{Key: "s1", Label: "a"}, Item{Key: "s2", Label: "b"}, Item{Key: "s3", Label: "c"})
	b.tabs[1].cursor, b.tabs[1].key = 2, "s3" // picked there before
	m, cmd := b.apply(Result{Goto: 2, Only: func(it Item) bool { return it.Key != "s1" }, OnlyLabel: "only s2, s3"})
	b = m.(browser)
	if b.cur != 1 || cmd == nil {
		t.Fatal("Goto did not switch and load")
	}
	if it, _ := b.selected(); it.Key != "s2" {
		t.Fatalf("after Goto the cursor is on %q, want the top row s2", it.Key)
	}
	if !strings.Contains(b.View(), "only s2, s3") {
		t.Fatal("the Only label is not shown")
	}
	b, _ = press(b, "esc")
	if b.tabs[1].only != nil || len(b.shown(1)) != 3 {
		t.Fatal("esc did not clear Only")
	}
}

func TestBrowserExec(t *testing.T) {
	b := testBrowser(nil)
	m, cmd := b.apply(Result{Exec: exec.Command("true")})
	b = m.(browser)
	if cmd == nil {
		t.Fatal("Exec gave no command")
	}
	m, cmd = b.Update(execMsg{err: errors.New("exit status 1")})
	b = m.(browser)
	if cmd == nil || !b.tabs[0].loading || !b.statErr {
		t.Fatal("coming back from Exec should reload the tab and show the error")
	}
}

func TestBrowserCleansClusterText(t *testing.T) {
	b := testBrowser(nil)
	m, _ := b.apply(Result{Text: "ok \x1b]52;c;cGF3bmVk\x07\x1b[2Jdone \x1b[31mred\x1b[0m", TextTitle: "x\x1b]0;t\x07"})
	b = m.(browser)
	if got := b.text[0]; got != "ok done \x1b[31mred\x1b[0m" {
		t.Fatalf("text %q", got)
	}
	if b.textTtl != "x" {
		t.Fatalf("title %q", b.textTtl)
	}
	if got := b.row(Item{Label: "a\x1b]0;evil\x07b", Hint: "h\x1b[2J"}, false); strings.Contains(got, "]0;") || strings.Contains(got, "[2J") {
		t.Fatalf("row %q", got)
	}
}

// The first load's rows arrive cluster by cluster: the cursor stays on the
// top row, not on whichever cluster answered first.
func TestBrowserFirstLoadStaysOnTop(t *testing.T) {
	b := newBrowser(BrowserOptions{Title: "t", Views: []View{{Name: "pods", Load: noLoad}}})
	b = startLoad(b, 0)
	gen := b.tabs[0].gen
	m, _ := b.Update(partialMsg{tab: 0, gen: gen, items: []Item{{Key: "u", Label: "use  app/a"}}, s: newLoadStream()})
	b = m.(browser)
	m, _ = b.Update(loadedMsg{tab: 0, gen: gen, items: []Item{{Key: "e", Label: "euw  app/a"}, {Key: "u", Label: "use  app/a"}}})
	b = m.(browser)
	if it, _ := b.selected(); it.Key != "e" {
		t.Fatalf("after the first load the cursor is on %q, want the top row", it.Key)
	}
}
