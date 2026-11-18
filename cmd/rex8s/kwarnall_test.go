package main

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
)

// fakeWarnSource is a watch (kwatch.Hub) holding a list of events.
type fakeWarnSource struct {
	mu     sync.Mutex
	events []string // event JSON
	err    error
	no     bool // doesn't serve the cluster
	subs   []chan struct{}
	args   []string
}

func (f *fakeWarnSource) Serve(_ kube.Context, args []string) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.args = args
	if f.no {
		return nil, false, nil
	}
	if f.err != nil {
		return nil, true, f.err
	}
	return []byte(`{"kind":"List","items":[` + strings.Join(f.events, ",") + `]}`), true, nil
}

func (f *fakeWarnSource) Notify(_ kube.Context, _ []string, ch chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs = append(f.subs, ch)
}

// set changes the list (or the error) and wakes the watchers, as a watch
// event does.
func (f *fakeWarnSource) set(err error, events ...string) {
	f.mu.Lock()
	f.err = err
	if err == nil {
		f.events = events
	}
	subs := f.subs
	f.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// waitFor waits until the output has n lines.
func (l *lockedBuf) waitFor(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		lines := strings.Split(strings.TrimSpace(l.String()), "\n")
		if l.String() == "" {
			lines = nil
		}
		if len(lines) >= n {
			return lines
		}
		if time.Now().After(deadline) {
			t.Fatalf("want %d lines, got:\n%s", n, l.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// kwarn -A on a watch: the first read prints the events oldest first; a
// change prints only what changed; a failure is said once, and so is the
// recovery.
func TestWatchWarnings(t *testing.T) {
	e1 := warnEventJSON("u1", "Back-off restarting failed container", 5, "2026-11-04T10:00:00Z")
	e2 := warnEventJSON("u2", "Readiness probe failed", 1, "2026-11-04T10:00:01Z")
	src := &fakeWarnSource{events: []string{e2, e1}}
	seen := newWarnSeen()
	clock := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	seen.now = func() time.Time { clock = clock.Add(2 * time.Minute); return clock } // never within the minute
	var out lockedBuf
	stop := make(chan struct{})
	done := make(chan bool)
	go func() { done <- watchWarnings(src, kube.Context{Name: "use", Short: "use"}, &out, seen, stop) }()

	lines := out.waitFor(t, 2)
	if !strings.Contains(lines[0], "Back-off") || !strings.Contains(lines[1], "Readiness") {
		t.Fatalf("not oldest first:\n%s", out.String())
	}
	if strings.Join(src.args, " ") != "get events -A --field-selector type=Warning -o json" {
		t.Errorf("query %q", src.args)
	}

	e1b := warnEventJSON("u1", "Back-off restarting failed container", 9, "2026-11-04T10:29:00Z")
	e3 := warnEventJSON("u3", "Failed to pull image", 1, "2026-11-04T10:29:01Z")
	src.set(nil, e2, e1b, e3)
	lines = out.waitFor(t, 4)
	if len(lines) != 4 || !strings.Contains(lines[2], "x9") || !strings.Contains(lines[3], "pull image") {
		t.Fatalf("want only the changed and the new event:\n%s", out.String())
	}

	src.set(errors.New("unreachable (k8s.example.com:6443)"))
	out.waitFor(t, 5)
	src.set(errors.New("unreachable (k8s.example.com:6443)"))
	time.Sleep(3 * warnSettle) // read again, failing the same way: nothing new
	src.set(nil, e2, e1b, e3)
	lines = out.waitFor(t, 6)
	if len(lines) != 6 || !strings.Contains(lines[4], "watch failed (unreachable (k8s.example.com:6443)); still trying") ||
		!strings.Contains(lines[5], "watching again") {
		t.Fatalf("failure and recovery:\n%s", out.String())
	}

	close(stop)
	if !<-done {
		t.Error("stopped: want true")
	}
}

// A cluster the watch can't serve (a legacy auth-provider) goes back to
// kubectl -w at once.
func TestWatchWarningsNotServed(t *testing.T) {
	var out lockedBuf
	if watchWarnings(&fakeWarnSource{no: true}, kube.Context{Name: "old"}, &out, newWarnSeen(), nil) {
		t.Fatal("not served: want false")
	}
	if out.String() != "" {
		t.Errorf("printed %q", out.String())
	}
}

// The live views share watchHub: off in demo mode and with watch: false;
// otherwise the reader's Source.
func TestWatchHubGate(t *testing.T) {
	off := false
	r := &fleet.Reader{}
	hub, stop := (&app{cfg: &config.Config{Watch: &off}}).watchHub(kube.Client{}, r)
	stop()
	if hub != nil || r.Source != nil {
		t.Fatal("watch: false still watches")
	}
	t.Setenv("REX8S_DEMO", t.TempDir())
	if hub, _ := (&app{cfg: &config.Config{}}).watchHub(kube.Client{}, r); hub != nil {
		t.Fatal("demo mode watches")
	}
	t.Setenv("REX8S_DEMO", "")
	hub, stop = (&app{cfg: &config.Config{}}).watchHub(kube.Client{Timeout: 7 * time.Second}, r)
	defer stop()
	if hub == nil || r.Source != fleet.Source(hub) || hub.FirstWait != 7*time.Second {
		t.Fatalf("hub %v, source %v", hub, r.Source)
	}
}

func TestWarnTrouble(t *testing.T) {
	c := kube.Context{Short: "use"}
	if got := warnTrouble(c, errors.New("Unauthorized")); !strings.Contains(got, "login expired: kauth use") {
		t.Errorf("auth: %q", got)
	}
	a := warnTrouble(c, kube.ErrTimeout)
	b := warnTrouble(c, errors.Join(kube.ErrTimeout, errors.New("no answer in 31s")))
	if a != b || !strings.Contains(a, "no answer yet") {
		t.Errorf("a timeout is one line however long it lasts: %q %q", a, b)
	}
}
