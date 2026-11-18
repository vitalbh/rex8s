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
	streamWarnings(out, os.Stdout, ns == "", nil)
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
	return true
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
