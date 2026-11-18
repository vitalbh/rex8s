package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func warnEventJSON(uid, msg string, count int, last string) string {
	m, _ := json.Marshal(msg)
	return fmt.Sprintf(`{"metadata":{"uid":%q,"namespace":"app"},"involvedObject":{"kind":"Pod","name":"api-1"},"reason":"BackOff","message":%s,"count":%d,"lastTimestamp":%q}`,
		uid, m, count, last)
}

// A watch that starts again relists the current events: with the same seen
// (kwarn -A keeps one per cluster across watches) they are not shown again;
// an event that changed is (B12).
func TestStreamWarningsAcrossWatches(t *testing.T) {
	seen := newWarnSeen()
	clock := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	seen.now = func() time.Time { return clock }
	e1 := warnEventJSON("u1", "Back-off restarting failed container", 5, "2026-11-04T10:00:00Z")
	e2 := warnEventJSON("u2", "Readiness probe failed", 1, "2026-11-04T10:00:01Z")

	var first strings.Builder
	streamWarnings(strings.NewReader(e1+"\n"+e2+"\n"), &first, true, seen)
	if n := strings.Count(first.String(), "\n"); n != 2 {
		t.Fatalf("first watch: %d lines\n%s", n, first.String())
	}

	clock = clock.Add(30 * time.Minute) // the API server ended the watch; it starts again
	var relist strings.Builder
	e1b := warnEventJSON("u1", "Back-off restarting failed container", 9, "2026-11-04T10:29:00Z")
	streamWarnings(strings.NewReader(e1+"\n"+e2+"\n"+e1b+"\n"), &relist, true, seen)
	got := relist.String()
	if strings.Count(got, "\n") != 1 || !strings.Contains(got, "x9") {
		t.Fatalf("relist: want only the changed event, got\n%s", got)
	}

	// within the minute a changing event is not shown again
	e1c := warnEventJSON("u1", "Back-off restarting failed container", 10, "2026-11-04T10:29:10Z")
	var again strings.Builder
	streamWarnings(strings.NewReader(e1c+"\n"), &again, true, seen)
	if again.Len() != 0 {
		t.Fatalf("shown twice in a minute: %q", again.String())
	}
}

func TestWarnSeenBounded(t *testing.T) {
	seen := newWarnSeen()
	clock := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	seen.now = func() time.Time { return clock }
	for i := range seenMax + 10 {
		clock = clock.Add(time.Millisecond)
		seen.show(fmt.Sprint("u", i), clock, 1)
	}
	if n := len(seen.m); n > seenMax {
		t.Fatalf("%d entries kept, max %d", n, seenMax)
	}
	if !seen.show("u0", clock, 1) {
		t.Error("the oldest entry should have gone")
	}
	if seen.show(fmt.Sprint("u", seenMax+9), clock, 1) {
		t.Error("the newest entry should stay")
	}
	clock = clock.Add(seenTTL + time.Minute)
	seen.show("new", clock, 1)
	if n := len(seen.m); n != 1 {
		t.Fatalf("after the TTL: %d entries, want 1", n)
	}
}

// Event text comes from apps: escape sequences must not reach the terminal
// (B20).
func TestStreamWarningsCleansText(t *testing.T) {
	ev := warnEventJSON("u1", "Readiness probe failed: \x1b]52;c;aGk=\x07\x1b[2Jbody", 1, "2026-11-04T10:00:00Z")
	var out strings.Builder
	streamWarnings(strings.NewReader(ev+"\n"), &out, true, nil)
	got := out.String()
	if strings.Contains(got, "]52") || strings.Contains(got, "[2J") {
		t.Fatalf("escape sequence passed: %q", got)
	}
	if !strings.Contains(got, "Readiness probe failed: body") {
		t.Fatalf("message lost: %q", got)
	}
}

func TestLogRef(t *testing.T) {
	for _, c := range []struct{ in, kind, name, title string }{
		{"web-7d9f", "", "web-7d9f", "use/web-7d9f"},
		{"job/migrate", "job", "migrate", "use/migrate (job)"},
	} {
		k, n := logRef(c.in)
		if k != c.kind || n != c.name || logTitle("use", k, n) != c.title {
			t.Errorf("%q: %q %q %q", c.in, k, n, logTitle("use", k, n))
		}
	}
}
