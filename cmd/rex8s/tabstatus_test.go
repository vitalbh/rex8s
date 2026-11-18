package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/logstream"
)

func recordStatus() (*tabStatus, *[]string) {
	var got []string
	s := &tabStatus{stop: func() {}}
	s.send = func(state, kind, msg string) { got = append(got, strings.TrimRight(state+" "+kind+" "+msg, " ")) }
	return s, &got
}

func TestTabStatusOnlyChanges(t *testing.T) {
	s, got := recordStatus()
	s.Set("idle", "following 3 pods")
	s.Set("idle", "following 3 pods")
	s.Blocked("auth", "login expired")
	s.Set("error", "x")
	s.Clear()
	s.Clear() // nothing left to clear
	want := "idle  following 3 pods|blocked auth login expired|error  x|clear"
	if strings.Join(*got, "|") != want {
		t.Errorf("sent %q\nwant %q", strings.Join(*got, "|"), want)
	}
}

func TestStatusLine(t *testing.T) {
	use := kube.Context{Short: "use"}
	cases := []struct {
		secs                  []section
		state, kind, wantText string
	}{
		{[]section{{name: "Nodes"}, {name: "Pods"}, {skip: true, bad: true}}, "idle", "", "use: healthy"},
		{[]section{{name: "Pods", bad: true, failing: 3}, {name: "Flux", bad: true}, {name: "Nodes"}}, "error", "", "use: 3 failing pods · Flux"},
		{[]section{{name: "Pods", bad: true, failing: 1}}, "error", "", "use: 1 failing pod"},
		{[]section{errSection("Nodes", errors.New("error: You must be logged in to the server (Unauthorized)")), {name: "Pods"}}, "blocked", "auth", "use: login expired · kauth use"},
	}
	for i, c := range cases {
		state, kind, msg := statusLine(use, c.secs)
		if state != c.state || kind != c.kind || msg != c.wantText {
			t.Errorf("%d: %s %s %q, want %s %s %q", i, state, kind, msg, c.state, c.kind, c.wantText)
		}
	}
}

func TestLogStatus(t *testing.T) {
	tab, got := recordStatus()
	l := newLogStatus(tab, "timeout")
	api := logstream.Spec{Ctx: "acme-prod-use", NS: "api", Selector: "app=api", Label: "use api"}
	web := logstream.Spec{Ctx: "acme-prod-euw", NS: "web", Selector: "app=web", Label: "euw web"}
	l.update(api, 3, "")
	l.update(web, 2, "")
	l.update(web, 0, "login expired · run kauth euw")
	l.update(api, 0, "no running pods")
	l.update(api, 3, "")
	l.update(web, 2, "")
	want := []string{
		"idle  following 3 pods · grep timeout",
		"idle  following 5 pods · grep timeout",
		"error  following 3 pods · grep timeout · euw web: login expired · run kauth euw",
		"error  euw web: login expired · run kauth euw (+1 stream)",
		"error  following 3 pods · grep timeout · euw web: login expired · run kauth euw",
		"idle  following 5 pods · grep timeout",
	}
	if strings.Join(*got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sent:\n%s\nwant:\n%s", strings.Join(*got, "\n"), strings.Join(want, "\n"))
	}
}

func TestWarnWindow(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	w := newWarnWindow(10 * time.Minute)
	w.now = func() time.Time { return now }
	w.add("a", now.Add(-time.Minute))
	w.add("a", now.Add(-30*time.Second)) // the same event again: counted once
	w.add("b", now.Add(-20*time.Minute)) // relisted, too old
	w.add("c", now.Add(-9*time.Minute))
	if n := w.count(); n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	now = now.Add(2 * time.Minute) // c falls out
	if n := w.count(); n != 1 {
		t.Errorf("count after 2m = %d, want 1", n)
	}
	if got := w.line("use"); got != "use: 1 warning in the last 10m" {
		t.Errorf("line = %q", got)
	}
}
