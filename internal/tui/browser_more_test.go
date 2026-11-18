package tui

import (
	"cmp"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func itemKeys(items []Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Key)
	}
	return out
}

// Space marks and moves down; ctrl+a marks every row shown, again none; a
// complete load drops the marks of rows that are gone, a partial one keeps
// them.
func TestBrowserMarks(t *testing.T) {
	b := testBrowser(nil)
	b, _ = press(b, " ")
	if !b.tabs[0].marks["a"] || b.tabs[0].key != "b" {
		t.Fatalf("space: marks %v cursor %q", b.tabs[0].marks, b.tabs[0].key)
	}
	if v := b.View(); !strings.Contains(v, "1 marked") || !strings.Contains(v, "●") {
		t.Fatalf("a mark is not shown:\n%s", v)
	}
	m, _ := b.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	b = m.(browser)
	if len(b.tabs[0].marks) != 3 {
		t.Fatalf("ctrl+a: %v", b.tabs[0].marks)
	}
	m, _ = b.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	if b = m.(browser); len(b.tabs[0].marks) != 0 {
		t.Fatalf("ctrl+a again: %v", b.tabs[0].marks)
	}
	b, _ = press(b, "/", "a", "p", "p", "enter")
	m, _ = b.Update(tea.KeyMsg{Type: tea.KeyCtrlA}) // only the rows shown
	if b = m.(browser); len(b.tabs[0].marks) != 2 || b.tabs[0].marks["c"] {
		t.Fatalf("ctrl+a under a filter: %v", b.tabs[0].marks)
	}
	b.tabs[0].loading = true
	m, _ = b.Update(partialMsg{tab: 0, gen: b.tabs[0].gen, items: []Item{{Key: "a"}}, s: newLoadStream()})
	if b = m.(browser); len(b.tabs[0].marks) != 2 {
		t.Fatal("a partial load dropped marks")
	}
	b = loaded(b, 0, Item{Key: "a", Label: "app/api-1"}, Item{Key: "c", Label: "db/pg-0"})
	if !b.tabs[0].marks["a"] || b.tabs[0].marks["b"] {
		t.Fatalf("after the load: %v (b is gone)", b.tabs[0].marks)
	}
}

// A Multi action with marked rows asks once, listing them, then runs
// ActionMany with all of them; without marks it acts on the cursor's row.
func TestBrowserMultiAction(t *testing.T) {
	var got []string
	b := newBrowser(BrowserOptions{Title: "t",
		Action:     func(_ int, it Item, _ string) Result { got = []string{"one:" + it.Key}; return Result{} },
		ActionMany: func(_ int, its []Item, _ string) Result { got = itemKeys(its); return Result{} },
		Views:      []View{{Name: "pods", Load: noLoad, Actions: []Action{{Key: "ctrl+d", Label: "delete", Confirm: true, Multi: true}}}}})
	b.tabs[0].gen = 1
	b = loaded(b, 0, Item{Key: "a", Name: "pod x/a on use"}, Item{Key: "b", Name: "pod x/b on euw (PROD)"}, Item{Key: "c", Name: "pod x/c on use"})
	b, _ = press(b, " ", " ")
	m, _ := b.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	b = m.(browser)
	if b.confirm == nil || len(b.confirm.its) != 2 {
		t.Fatalf("confirm: %+v", b.confirm)
	}
	v := b.View()
	for _, want := range []string{"delete these 2 objects?", "pod x/a on use", "pod x/b on euw", "PROD", "delete 2 objects (PROD)?"} {
		if !strings.Contains(v, want) {
			t.Errorf("the confirmation lacks %q:\n%s", want, v)
		}
	}
	b, cmd := press(b, "y")
	cmd()
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("ActionMany got %v", got)
	}
	b.busy = ""
	b, _ = press(b, "esc") // clears the marks
	m, _ = b.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	b = m.(browser)
	_, cmd = press(b, "y")
	cmd()
	if !slices.Equal(got, []string{"one:c"}) {
		t.Fatalf("without marks: %v", got)
	}
}

// Shift+letters sort (again: reverse), rows without a Key stay last, and
// the header says the sort.
func TestBrowserSort(t *testing.T) {
	byLabel := func(a, b Item) int { return cmp.Compare(a.Label, b.Label) }
	b := newBrowser(BrowserOptions{Title: "t", Views: []View{{Name: "pods", Load: noLoad,
		Header: func() string { return "NAME" }, Sorts: []SortKey{{Key: "N", Name: "name", Cmp: byLabel}}}}})
	b.tabs[0].gen = 1
	b = loaded(b, 0, Item{Key: "b", Label: "b"}, Item{Key: "", Label: "skipped use"}, Item{Key: "a", Label: "a"}, Item{Key: "c", Label: "c"})
	b, _ = press(b, "N")
	if got := itemKeys(b.shown(0)); !slices.Equal(got, []string{"a", "b", "c", ""}) {
		t.Fatalf("N: %q", got)
	}
	if !strings.Contains(b.View(), "[sort: name ↑]") {
		t.Fatal("the header does not say the sort")
	}
	b, _ = press(b, "N")
	if got := itemKeys(b.shown(0)); !slices.Equal(got, []string{"c", "b", "a", ""}) {
		t.Fatalf("N again: %q", got)
	}
	b = loaded(b, 0, Item{Key: "d", Label: "d"}, Item{Key: "a", Label: "a"})
	if got := itemKeys(b.shown(0)); !slices.Equal(got, []string{"d", "a"}) {
		t.Fatalf("a refresh is sorted too: %q", got)
	}
}

// A Choice asks first; its pick runs Then in the background.
func TestBrowserChoose(t *testing.T) {
	b := testBrowser(nil)
	picked := -1
	m, _ := b.apply(Result{Choose: &Choice{Title: "which container?", Options: []string{"app", "proxy"},
		Then: func(i int) Result { picked = i; return Result{Status: "ok"} }}})
	b = m.(browser)
	if b.mode != modeChoose || !strings.Contains(b.View(), "which container?") {
		t.Fatal("no picker")
	}
	b, cmd := press(b, "down", "enter")
	m, _ = b.Update(cmd())
	if b = m.(browser); picked != 1 || b.status != "ok" || b.mode != modeList {
		t.Fatalf("picked %d status %q", picked, b.status)
	}
	m, _ = b.apply(Result{Choose: &Choice{Options: []string{"a", "b"}, Then: func(int) Result { picked = 9; return Result{} }}})
	b, _ = press(m.(browser), "esc")
	if b.mode != modeList || picked == 9 {
		t.Fatal("esc did not cancel the picker")
	}
}

// ":" runs Command; an Open adds a tab after the fixed ones (a second one
// replaces it), and esc goes back and closes it.
func TestBrowserCommandOpensTab(t *testing.T) {
	var typed string
	b := testBrowser(nil)
	b.o.Complete = func(p string) []string {
		if strings.HasPrefix("ingresses", p) {
			return []string{"ingresses  (ing)"}
		}
		return nil
	}
	b.o.Command = func(text string) Result {
		typed = text
		return Result{Open: &View{Name: ":" + text, Load: noLoad}}
	}
	b, _ = press(b, ":", "i", "n")
	if !strings.Contains(b.View(), "ingresses") {
		t.Fatal("no suggestion shown")
	}
	b, _ = press(b, "tab")
	b, cmd := press(b, "enter")
	m, _ := b.Update(cmd())
	b = m.(browser)
	if typed != "ingresses" || len(b.tabs) != 3 || b.cur != 2 || b.o.Views[2].Name != ":ingresses" {
		t.Fatalf("typed %q, %d tabs, on %d", typed, len(b.tabs), b.cur)
	}
	m, _ = b.apply(Result{Open: &View{Name: ":cm", Load: noLoad}})
	if b = m.(browser); len(b.tabs) != 3 || b.o.Views[2].Name != ":cm" {
		t.Fatal("a second Open should replace the opened tab")
	}
	b, _ = press(b, "esc", "esc")
	if len(b.tabs) != 2 || b.cur != 0 {
		t.Fatalf("esc: %d tabs, on %d", len(b.tabs), b.cur)
	}
}

// A Goto remembers where it came from: backspace (or esc) goes back there,
// with that tab's filter, and the tab left loses what the Goto set.
func TestBrowserBack(t *testing.T) {
	b := testBrowser(nil)
	b, _ = press(b, "/", "w", "e", "b", "enter")
	m, _ := b.apply(Result{Goto: 2, Only: func(Item) bool { return true }, OnlyLabel: "pods of x"})
	b = m.(browser)
	if b.cur != 1 || !strings.Contains(b.View(), "esc back") {
		t.Fatal("no Goto")
	}
	b, _ = press(b, "backspace")
	if b.cur != 0 || b.tabs[0].filter != "web" || b.tabs[1].only != nil || len(b.history) != 0 {
		t.Fatalf("back: on %d filter %q", b.cur, b.tabs[0].filter)
	}
	if it, _ := b.selected(); it.Key != "b" {
		t.Fatalf("back: the cursor is on %q", it.Key)
	}
}

// The ":" suggestions are a list: ↑↓ moves the highlight, enter opens the
// highlighted one when what was typed isn't a name itself.
func TestBrowserCommandSuggestionList(t *testing.T) {
	var typed string
	b := testBrowser(nil)
	b.o.Complete = func(p string) []string {
		return []string{"configmaps  (cm)", "cronjobs  (cj)", "customresourcedefinitions  (crd)"}
	}
	b.o.Command = func(text string) Result { typed = text; return Result{} }
	b, _ = press(b, ":", "c")
	v := b.View()
	if !strings.Contains(v, "configmaps") || !strings.Contains(v, "customresourcedefinitions") || !strings.Contains(v, "(cj)") {
		t.Fatalf("the suggestions should be listed whole:\n%s", v)
	}
	b, _ = press(b, "down", "down", "up")
	_, cmd := press(b, "enter")
	cmd()
	if typed != "cronjobs" {
		t.Fatalf("enter opened %q, want the highlighted cronjobs", typed)
	}
}
