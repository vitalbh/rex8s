package tui

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// litKeys: the rows of the current tab lit now, "+key" new, "~key" changed.
func litKeys(b browser) []string {
	var out []string
	for _, it := range b.shown(b.cur) {
		if on, _, isNew := b.litOf(it); on {
			sign := "~"
			if isNew {
				sign = "+"
			}
			out = append(out, sign+it.Key)
		}
	}
	return out
}

// A refresh lights the rows it adds and the ones whose Sig changed, not an
// age ticking in the label, not rows that only moved, nothing on the first
// load; the lights fade by themselves.
func TestBrowserLightsChanges(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	b := newBrowser(BrowserOptions{Title: "t", Views: []View{{Name: "pods", Load: noLoad}}})
	b.now = func() time.Time { return now }
	b.tabs[0].gen = 1
	b = loaded(b, 0,
		Item{Key: "a", Label: "api   Running 1m", Sig: "Running"},
		Item{Key: "b", Label: "web   Running 1m", Sig: "Running"},
		Item{Key: "c", Label: "db    Running 1m", Sig: "Running"},
		Item{Key: "x", Label: "plain"})
	if got := litKeys(b); got != nil {
		t.Fatalf("the first load lit %v", got)
	}
	b, _ = press(b, "down") // the cursor on web
	b = loaded(b, 0,
		Item{Key: "c", Label: "db    Running 2m", Sig: "Running"},            // moved, its age ticked
		Item{Key: "b", Label: "web   CrashLoop 2m", Sig: "CrashLoopBackOff"}, // changed
		Item{Key: "a", Label: "api   Running 2m", Sig: "Running"},
		Item{Key: "x", Label: "plain, other text"}, // no Sig: only new rows light
		Item{Key: "d", Label: "new   Pending 0s", Sig: "Pending"})
	if got := litKeys(b); !slices.Equal(got, []string{"~b", "+d"}) {
		t.Fatalf("lit %v, want ~b +d", got)
	}
	if it, _ := b.selected(); it.Key != "b" {
		t.Fatalf("the cursor moved to %q", it.Key)
	}
	if !b.fading {
		t.Fatal("no fadeMsg on its way: the lights would stay until a key")
	}
	// the marker shows on a lit row the cursor is not on
	if v := b.View(); !strings.Contains(v, "  +") {
		t.Fatalf("no + marker on the new row:\n%s", v)
	}

	now = now.Add(4 * time.Second) // past the strong part: the marker only
	if _, strong, _ := b.litOf(Item{Key: "d"}); strong {
		t.Fatal("still strong after 4s")
	}
	m, cmd := b.Update(fadeMsg{})
	b = m.(browser)
	if cmd == nil || len(litKeys(b)) != 2 {
		t.Fatalf("faded too soon: %v", litKeys(b))
	}
	now = now.Add(2 * time.Second)
	m, cmd = b.Update(fadeMsg{})
	b = m.(browser)
	if cmd != nil || len(b.tabs[0].lit) != 0 || b.fading {
		t.Fatalf("still lit after %v: %v", litFor, b.tabs[0].lit)
	}
	if strings.Contains(b.View(), "  +") {
		t.Fatal("the marker stayed")
	}

	// a row that goes takes its light with it; one lit stays lit through a
	// refresh that does not change it
	b = loaded(b, 0, Item{Key: "a", Sig: "Running"}, Item{Key: "e", Sig: "Pending"})
	b = loaded(b, 0, Item{Key: "e", Sig: "Pending"})
	if got := litKeys(b); !slices.Equal(got, []string{"+e"}) || len(b.tabs[0].lit) != 1 {
		t.Fatalf("lit %v (%v)", got, b.tabs[0].lit)
	}
}

// Partial rows of a first load (clusters answering one by one) light
// nothing; a later load's do.
func TestBrowserLightsNotOnFirstPartials(t *testing.T) {
	b := newBrowser(BrowserOptions{Title: "t", Views: []View{{Name: "pods", Load: noLoad}}})
	b.tabs[0].gen, b.tabs[0].loading = 1, true
	partial := func(items ...Item) {
		m, _ := b.Update(partialMsg{tab: 0, gen: b.tabs[0].gen, items: items, s: newLoadStream()})
		b = m.(browser)
	}
	partial(Item{Key: "a", Sig: "1"})
	partial(Item{Key: "a", Sig: "1"}, Item{Key: "b", Sig: "1"})
	b = loaded(b, 0, Item{Key: "a", Sig: "1"}, Item{Key: "b", Sig: "1"})
	if got := litKeys(b); got != nil {
		t.Fatalf("first load lit %v", got)
	}
	b.tabs[0].loading = true
	partial(Item{Key: "a", Sig: "2"}, Item{Key: "b", Sig: "1"})
	if got := litKeys(b); !slices.Equal(got, []string{"~a"}) {
		t.Fatalf("a later partial lit %v", got)
	}
}

// Hidden rows (a CronJob's runs) show only under an Only that accepts them:
// not in the list, not through the filter.
func TestBrowserHiddenRows(t *testing.T) {
	b := newBrowser(BrowserOptions{Title: "t", Views: []View{{Name: "jobs", Load: noLoad}}})
	b.tabs[0].gen = 1
	b = loaded(b, 0, Item{Key: "cron/export", Label: "cron export"},
		Item{Key: "job/export-2", Label: "job export-2", Hidden: true},
		Item{Key: "job/export-1", Label: "job export-1", Hidden: true},
		Item{Key: "job/migrate", Label: "job migrate"})
	keys := func() []string {
		var out []string
		for _, it := range b.shown(0) {
			out = append(out, it.Key)
		}
		return out
	}
	if got := keys(); !slices.Equal(got, []string{"cron/export", "job/migrate"}) {
		t.Fatalf("shown %v", got)
	}
	b, _ = press(b, "/", "e", "x", "p", "enter")
	if got := keys(); !slices.Equal(got, []string{"cron/export"}) {
		t.Fatalf("filtered %v", got)
	}
	m, _ := b.apply(Result{Goto: 1, Only: func(it Item) bool { return strings.HasPrefix(it.Key, "job/export-") }, OnlyLabel: "runs of export"})
	b = m.(browser)
	if got := keys(); !slices.Equal(got, []string{"job/export-2", "job/export-1"}) {
		t.Fatalf("under Only %v", got)
	}
	b, _ = press(b, "esc") // back to the list, the filter it had
	if got := keys(); !slices.Equal(got, []string{"cron/export"}) || b.tabs[0].filter != "exp" {
		t.Fatalf("back: %v filter %q", got, b.tabs[0].filter)
	}
}
