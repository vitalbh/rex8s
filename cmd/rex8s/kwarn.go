package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kwarn streams only Warning events (OOMKilled, BackOff, failed probes,
// image pulls…), across all namespaces by default.
//
//	kwarn        all namespaces of this pane's cluster
//	kwarn -n     only this pane's namespace
//	kwarn -A [CLUSTER...]  every cluster (or the ones named), one stream
func (a *app) kwarn(args []string) error {
	fs := flag.NewFlagSet("kwarn", flag.ContinueOnError)
	nsOnly := fs.Bool("n", false, "only the pane's namespace")
	every := fs.Bool("A", false, "every cluster (or the ones named), one stream")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *every {
		return a.kwarnAll(fs.Args())
	}
	c, err := kube.Current(a.cfg, "", "")
	if err != nil {
		return err
	}
	ns := ""
	if *nsOnly && !c.AllNS {
		ns = c.Namespace
		if ns == "" {
			ns = "default"
		}
	}
	return a.warnings(c.Name, ns)
}

// warnings streams Warning events of ctxName (ns "" = all namespaces). It is
// also the bottom pane of the k9s layout.
func (a *app) warnings(ctxName, ns string) error {
	if ns == kube.AllNamespaces {
		ns = ""
	}
	c, err := a.context(ctxName)
	if err != nil {
		return err
	}
	scope, label := "all namespaces", "*"
	if ns != "" {
		scope, label = ns, ns
	}
	rex.Rename("warnings " + c.Short + "/" + label)
	a.tint(c)
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	args := []string{"get", "events", "--field-selector", "type=Warning", "-w", "-o", "json"}
	if ns == "" {
		args = append(args, "-A")
	} else {
		args = append(args, "-n", ns)
	}
	cmd := cl.Command(context.Background(), ctxName, args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	fmt.Println(tui.Bold.Render("Warnings") + tui.Dim.Render(" · "+c.Short+" · "+scope+" · repeats shown at most once a minute"))
	seen := newWarnSeen()
	var stop func()
	seen.shown, stop = warnStatus(c.Short)
	defer stop()
	streamWarnings(out, os.Stdout, ns == "", seen)
	err = cmd.Wait()
	waitClose("watch ended")
	return err
}

// streamWarnings formats a stream of event JSON objects. seen decides which
// to show (nil: a new one); keep it across watches of the same cluster, so
// a watch that starts again does not show its relist a second time. Text
// from the cluster goes through tui.Clean: an app's output in a message
// cannot send escape sequences to the terminal.
func streamWarnings(r io.Reader, w io.Writer, showNS bool, seen *warnSeen) {
	if seen == nil {
		seen = newWarnSeen()
	}
	dec := json.NewDecoder(r)
	for {
		var e kube.Event
		if err := dec.Decode(&e); err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintln(w, tui.Err.Render("decode: "+err.Error()))
			}
			return
		}
		when, count := e.When(), e.Count
		if e.Series != nil {
			count = e.Series.Count
		}
		if !seen.show(e.Metadata.UID, when, count) {
			continue
		}
		obj := kube.ShortKind(e.InvolvedObject.Kind) + "/" + e.InvolvedObject.Name
		if showNS {
			obj = e.Metadata.Namespace + " " + obj
		}
		reps := ""
		if count > 1 {
			reps = tui.Dim.Render(fmt.Sprintf(" x%d", count))
		}
		fmt.Fprintf(w, "%s %s %s%s %s\n",
			tui.Dim.Render(when.Local().Format("15:04:05")),
			tui.Warn.Render(oneLine(tui.Clean(e.Reason))), oneLine(tui.Clean(obj)), reps,
			truncate(oneLine(tui.Clean(e.Message)), 240))
	}
}

// warnSeen remembers the Warning events already shown: an event comes back
// unchanged when a watch starts again (it relists), and a repeating one
// (BackOff) is updated constantly. It is bounded: entries not shown for
// seenTTL go (the API server keeps events an hour by default), and past
// seenMax the oldest go.
type warnSeen struct {
	m      map[string]seenEvent // by event UID
	now    func() time.Time
	pruned time.Time
	// shown, when set, hears of every event printed (the tab status count)
	shown func(uid string, when time.Time)
}

type seenEvent struct {
	shown time.Time // when it was printed
	when  time.Time // the event's last occurrence then
	count int
}

const (
	seenTTL = 2 * time.Hour
	seenMax = 5000
)

func newWarnSeen() *warnSeen {
	return &warnSeen{m: map[string]seenEvent{}, now: time.Now}
}

// show reports whether the event should be printed, and if so records it:
// not when it is unchanged since it was shown, and at most once a minute.
func (s *warnSeen) show(uid string, when time.Time, count int) bool {
	now := s.now()
	if p, ok := s.m[uid]; ok {
		if p.count == count && p.when.Equal(when) {
			return false
		}
		if now.Sub(p.shown) < time.Minute {
			return false
		}
	}
	s.m[uid] = seenEvent{shown: now, when: when, count: count}
	s.prune(now)
	if s.shown != nil {
		s.shown(uid, when)
	}
	return true
}

// warnWindow counts the Warning events that occurred in the last span, for
// the tab status of kwarn: "use: 3 warnings in the last 10m". An event
// counts once however often it repeats; a relisted old one doesn't count.
type warnWindow struct {
	mu   sync.Mutex
	span time.Duration
	last map[string]time.Time // event UID → its last occurrence
	now  func() time.Time
}

const warnSpan = 10 * time.Minute

func newWarnWindow(span time.Duration) *warnWindow {
	return &warnWindow{span: span, last: map[string]time.Time{}, now: time.Now}
}

func (w *warnWindow) add(uid string, when time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if when.IsZero() {
		when = w.now()
	}
	if when.After(w.last[uid]) {
		w.last[uid] = when
	}
}

func (w *warnWindow) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	cutoff := w.now().Add(-w.span)
	for uid, t := range w.last {
		if t.Before(cutoff) {
			delete(w.last, uid)
		}
	}
	return len(w.last)
}

func (w *warnWindow) line(who string) string {
	n := w.count()
	if n == 0 {
		return who + ": no warnings in the last " + shortDur(w.span)
	}
	return who + ": " + plural(n, "warning") + " in the last " + shortDur(w.span)
}

// warnStatus reports the window on the tab whenever a warning is shown and
// every 30s (old ones fall out): done while there are some, idle without.
// The returned func goes in warnSeen.shown; stop ends the reports and
// clears the status.
func warnStatus(who string) (shown func(string, time.Time), stop func()) {
	tab := newTabStatus()
	w := newWarnWindow(warnSpan)
	report := func() {
		if w.count() > 0 {
			tab.Set("done", w.line(who))
		} else {
			tab.Set("idle", w.line(who))
		}
	}
	report()
	quit := make(chan struct{})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				report()
			case <-quit:
				return
			}
		}
	}()
	var once sync.Once
	return func(uid string, when time.Time) { w.add(uid, when); report() },
		func() { once.Do(func() { close(quit); tab.Clear() }) }
}

func (s *warnSeen) prune(now time.Time) {
	if len(s.m) <= seenMax && now.Sub(s.pruned) < 10*time.Minute {
		return
	}
	s.pruned = now
	for uid, e := range s.m {
		if now.Sub(e.shown) > seenTTL {
			delete(s.m, uid)
		}
	}
	if len(s.m) <= seenMax {
		return
	}
	type aged struct {
		uid   string
		shown time.Time
	}
	all := make([]aged, 0, len(s.m))
	for uid, e := range s.m {
		all = append(all, aged{uid, e.shown})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].shown.Before(all[j].shown) })
	for _, a := range all[:len(all)-seenMax*3/4] {
		delete(s.m, a.uid)
	}
}
