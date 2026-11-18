package tui

import (
	"strings"
	"testing"
)

// View.Live says how live a watched tab is; "" shows the time instead.
func TestBrowserLiveLabel(t *testing.T) {
	label := "live · polling sa"
	b := newBrowser(BrowserOptions{Title: "t", Views: []View{
		{Name: "pods", Load: noLoad, Wake: make(chan struct{}, 1), Live: func() string { return label }},
	}})
	b.tabs[0].gen = 1
	b = loaded(b, 0, Item{Key: "a", Label: "app/api-1"})
	if !strings.Contains(b.View(), "1 rows · live · polling sa") {
		t.Fatalf("live label:\n%s", b.View())
	}
	label = ""
	if v := b.View(); strings.Contains(v, "live") || !strings.Contains(v, b.tabs[0].loadedAt.Format("15:04:05")) {
		t.Fatalf("no cluster watched:\n%s", v)
	}
}
