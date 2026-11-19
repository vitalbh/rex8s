package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/logstream"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// pickClusters: every cluster, or the ones named (fleet.Pick).
func (a *app) pickClusters(names []string) ([]kube.Context, error) {
	raw, err := kube.Load(a.src)
	if err != nil {
		return nil, err
	}
	return fleet.Pick(raw, a.cfg, names)
}

// kwarnAll: kwarn -A [CLUSTER...] — the Warning events of every cluster (or
// the ones named) in one stream, each line led by its cluster. The clusters
// are watched in this process (watchHub: one connection each, credential
// plugins run once per token); one it can't watch, or with watch: false,
// runs kubectl get -w, started again whenever it ends (API timeout, lost
// connection).
func (a *app) kwarnAll(names []string) error {
	clusters, err := a.pickClusters(names)
	if err != nil {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	hub, stopHub := a.watchHub(cl, nil)
	defer stopHub()
	w, prod := 0, false
	var shorts []string
	for _, c := range clusters {
		w = max(w, len(c.Short))
		prod = prod || c.Prod
		shorts = append(shorts, c.Short)
	}
	rex.Rename("warnings " + strings.Join(shorts, ","))
	if prod {
		a.tint(kube.Context{Prod: true})
	}
	fmt.Println(tui.Bold.Render("Warnings") + tui.Dim.Render(" · "+strings.Join(shorts, " · ")+" · all namespaces · repeats shown at most once a minute · ctrl+c to stop"))

	who := strings.Join(shorts, ",")
	if len(shorts) > 3 {
		who = fmt.Sprintf("%d clusters", len(shorts))
	}
	shown, stop := warnStatus(who)
	defer stop()

	var mu sync.Mutex
	var wg sync.WaitGroup
	for i, c := range clusters {
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(logstream.StreamColors[i%len(logstream.StreamColors)]))
		if c.Prod {
			style = style.Bold(true)
		}
		pw := logstream.NewPrefixWriter(style.Render(fmt.Sprintf("%-*s", w, c.Short))+" ", os.Stdout, &mu)
		wg.Add(1)
		go func() {
			defer wg.Done()
			seen := newWarnSeen() // across watches: a restarted watch relists
			seen.shown = shown
			seen.expired = func() { a.offerLogin(c) }
			if hub != nil && watchWarnings(hub, c, pw, seen, nil) {
				return
			}
			for {
				start := time.Now()
				cmd := cl.Command(context.Background(), c.Name, "get", "events", "-A",
					"--field-selector", "type=Warning", "-w", "-o", "json")
				var stderr strings.Builder
				cmd.Stderr = &stderr
				out, err := cmd.StdoutPipe()
				if err == nil {
					err = cmd.Start()
				}
				if err == nil {
					streamWarnings(out, pw, true, seen)
					err = cmd.Wait()
				}
				if kube.AuthExpired(fmt.Errorf("%s", stderr.String())) {
					a.offerLogin(c)
					fmt.Fprintln(pw, tui.Err.Render("login expired: kauth "+c.Short+" — this cluster stops here"))
					return
				}
				if time.Since(start) < 10*time.Second { // failing fast: say why, wait
					why := tui.Clean(lastLogLine(strings.TrimSpace(stderr.String())))
					if why == "" && err != nil {
						why = err.Error()
					}
					fmt.Fprintln(pw, tui.Dim.Render("watch failed ("+why+"); retrying in 30s"))
					time.Sleep(30 * time.Second)
				}
			}
		}()
	}
	wg.Wait()
	waitClose("watch ended")
	return nil
}

// warnQuery is the Warning events of every namespace as kwatch serves them
// (no -w: the hub watches).
var warnQuery = []string{"get", "events", "-A", "--field-selector", "type=Warning", "-o", "json"}

// warnSource is a watch kwarn -A reads (kwatch.Hub).
type warnSource interface {
	Serve(c kube.Context, args []string) (out []byte, ok bool, err error)
	Notify(c kube.Context, args []string, ch chan struct{})
}

const (
	// warnRecheck: a quiet watch is read anyway this often. A cluster that
	// went away sends no change; reading is how its failure shows.
	warnRecheck = 15 * time.Second
	// warnSettle: after a change, the time others have to come in before
	// the read (a relist is many changes).
	warnSettle = 200 * time.Millisecond
)

// watchWarnings prints c's Warning events from src as they change, until
// stop closes (nil: never), and returns true. False at once when src
// doesn't serve c: kubectl watches it. A failure is said when it starts
// and when it ends; the watch keeps trying by itself.
func watchWarnings(src warnSource, c kube.Context, w io.Writer, seen *warnSeen, stop <-chan struct{}) bool {
	ch := make(chan struct{}, 1)
	src.Notify(c, warnQuery, ch) // before the first read, to miss nothing
	tick := time.NewTicker(warnRecheck)
	defer tick.Stop()
	last := map[string]warnStamp{}
	failing := ""
	for {
		out, ok, err := src.Serve(c, warnQuery)
		if !ok {
			return false
		}
		var evs []kube.Event
		if err == nil {
			evs, err = kube.ParseEvents(out)
		}
		switch {
		case err != nil:
			if kube.AuthExpired(err) && seen.expired != nil {
				seen.expired()
			}
			if why := warnTrouble(c, err); why != failing {
				fmt.Fprintln(w, why)
				failing = why
			}
		default:
			if failing != "" {
				fmt.Fprintln(w, tui.Dim.Render("watching again"))
				failing = ""
			}
			last = printChanged(w, evs, last, seen)
		}
		select {
		case <-stop:
			return true
		case <-tick.C:
		case <-ch:
			select {
			case <-time.After(warnSettle):
			case <-stop:
				return true
			}
			select { // the changes that came in meanwhile: this read has them
			case <-ch:
			default:
			}
		}
	}
}

// warnStamp is an event as last read: a watch sends only what changed.
type warnStamp struct {
	when  time.Time
	count int
}

// printChanged prints the events that are new or changed since last (the
// previous read), oldest first, and returns what this read had. warnSeen
// alone is not enough: it forgets an event after seenTTL, and one still
// listed, unchanged, would be shown again.
func printChanged(w io.Writer, evs []kube.Event, last map[string]warnStamp, seen *warnSeen) map[string]warnStamp {
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].When().Before(evs[j].When()) })
	now := make(map[string]warnStamp, len(evs))
	for _, e := range evs {
		st := warnStamp{e.When(), warnCount(e)}
		now[e.Metadata.UID] = st
		if p, ok := last[e.Metadata.UID]; ok && p.count == st.count && p.when.Equal(st.when) {
			continue
		}
		printWarning(w, e, true, seen)
	}
	return now
}

// warnTrouble is the line that says why a watch can't read c.
func warnTrouble(c kube.Context, err error) string {
	if kube.AuthExpired(err) {
		return tui.Err.Render("login expired: kauth "+c.Short) + tui.Dim.Render(" — still watching")
	}
	why := "no answer yet"
	if !errors.Is(err, kube.ErrTimeout) { // its text counts the seconds
		why = tui.Clean(lastLogLine(err.Error()))
	}
	return tui.Dim.Render("watch failed (" + why + "); still trying")
}
