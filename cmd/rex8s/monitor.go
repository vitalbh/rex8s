package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// monitor polls Flux on every cluster, shows a dashboard and sends a
// notification when something starts failing (or recovers).
func (a *app) monitor(args []string) error {
	def := 2 * time.Minute
	if d, err := time.ParseDuration(a.cfg.MonitorInterval); err == nil && d > 0 {
		def = d
	}
	fs := flag.NewFlagSet("monitor", flag.ContinueOnError)
	interval := fs.Duration("interval", def, "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}

	raw, err := kube.Load(a.src)
	if err != nil {
		return err
	}
	clusters := dedupe(kube.Contexts(raw, a.cfg))
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	rex.Rename("flux monitor")
	rex.RenameWindow("Flux")

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)

	prev := map[string]clusterHealth{}
	first := true
	for {
		cur := pollAll(cl, clusters)
		draw(clusters, cur, *interval)
		alert(clusters, prev, cur, first)
		rex.RenameWindow(tabTitle(clusters, cur))
		prev, first = cur, false

		select {
		case <-sig:
			for _, c := range clusters {
				rex.Status(statusID(c), "clear", "", "")
			}
			return nil
		case <-time.After(*interval):
		}
	}
}

type clusterHealth struct {
	installed bool
	err       string
	items     []kube.FluxItem
	failing   map[string]kube.FluxItem
}

func pollAll(cl kube.Client, clusters []kube.Context) map[string]clusterHealth {
	var mu sync.Mutex
	var wg sync.WaitGroup
	out := map[string]clusterHealth{}
	for _, c := range clusters {
		wg.Add(1)
		go func(c kube.Context) {
			defer wg.Done()
			items, installed, err := cl.FluxItems(c.Name)
			h := clusterHealth{installed: installed, items: items, failing: map[string]kube.FluxItem{}}
			if err != nil {
				h.err = err.Error()
			}
			for _, it := range items {
				if it.Failing() {
					h.failing[it.Key()] = it
				}
			}
			mu.Lock()
			out[c.Name] = h
			mu.Unlock()
		}(c)
	}
	wg.Wait()
	return out
}

func draw(clusters []kube.Context, cur map[string]clusterHealth, interval time.Duration) {
	var b strings.Builder
	b.WriteString("\x1b[H\x1b[2J")
	b.WriteString(tui.Bold.Render("Flux monitor") + tui.Dim.Render(fmt.Sprintf("  ·  %s  ·  every %s  ·  ctrl+c to quit", time.Now().Format("15:04:05"), interval)) + "\n\n")
	for _, c := range clusters {
		h := cur[c.Name]
		tag := tui.Ok.Render("dev ")
		if c.Prod {
			tag = tui.Prod.Render("PROD")
		}
		b.WriteString(fmt.Sprintf("%s %-8s ", tag, c.Short))
		switch {
		case h.err != "":
			b.WriteString(accessError(c, errors.New(h.err)))
		case !h.installed:
			b.WriteString(tui.Dim.Render("flux not installed"))
		default:
			st := kube.Summarize(h.items)
			parts := []string{tui.Ok.Render(fmt.Sprintf("%d ready", st.Ready))}
			if st.Failing > 0 {
				parts = append(parts, tui.Err.Render(fmt.Sprintf("%d failing", st.Failing)))
			}
			if st.Progressing > 0 {
				parts = append(parts, tui.Warn.Render(fmt.Sprintf("%d reconciling", st.Progressing)))
			}
			if st.Suspended > 0 {
				parts = append(parts, tui.Warn.Render(fmt.Sprintf("%d suspended", st.Suspended)))
			}
			b.WriteString(strings.Join(parts, tui.Dim.Render(" · ")))
			n := 0
			for _, it := range h.items {
				if !it.Failing() {
					continue
				}
				if n == 5 {
					b.WriteString("\n" + tui.Dim.Render(fmt.Sprintf("              … and %d more", len(h.failing)-n)))
					break
				}
				n++
				b.WriteString("\n" + tui.Err.Render(fmt.Sprintf("              %s %s/%s", it.Kind, it.Namespace, it.Name)))
				if it.Message != "" {
					b.WriteString("\n" + tui.Muted.Render("                "+truncate(oneLine(it.Message), 110)))
				}
			}
		}
		b.WriteString("\n")
	}
	fmt.Print(b.String())
}

// tabTitle summarizes the health in the tab name: "Flux ok", "Flux 2 failing · use, euw".
func tabTitle(clusters []kube.Context, cur map[string]clusterHealth) string {
	failing := 0
	var bad []string
	for _, c := range clusters {
		h := cur[c.Name]
		if len(h.failing) > 0 || h.err != "" {
			bad = append(bad, c.Short)
		}
		failing += len(h.failing)
	}
	switch {
	case len(bad) == 0:
		return "Flux ok"
	case failing == 0:
		return "Flux · no access: " + strings.Join(bad, ", ")
	}
	return fmt.Sprintf("Flux %d failing · %s", failing, strings.Join(bad, ", "))
}

func statusID(c kube.Context) string {
	id := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-', r == '+':
			return r
		}
		return '_'
	}, c.Short)
	if len(id) > 32 {
		id = id[:32]
	}
	return "flux/" + id
}

// alert sends notifications for state changes and updates the per-cluster
// program status records.
func alert(clusters []kube.Context, prev, cur map[string]clusterHealth, first bool) {
	for _, c := range clusters {
		p, h := prev[c.Name], cur[c.Name]
		added, removed := diffKeys(p.failing, h.failing)

		switch {
		case h.err != "" && (first || p.err == ""):
			msg := "no access: " + truncate(h.err, 120)
			if kube.AuthExpired(errors.New(h.err)) {
				msg = "login expired · run kauth " + c.Short
			}
			rex.Notify("Flux · "+c.Short, msg)
		case h.err == "" && p.err != "" && !first:
			rex.Notify("Flux · "+c.Short, "reachable again")
		}
		if len(added) > 0 {
			it := h.failing[added[0]]
			msg := fmt.Sprintf("%s %s/%s failing", it.Kind, it.Namespace, it.Name)
			if len(added) > 1 {
				msg = fmt.Sprintf("%d resources started failing (%s %s/%s, …)", len(added), it.Kind, it.Namespace, it.Name)
			}
			rex.Notify("Flux · "+c.Short, msg)
		}
		if !first && len(h.failing) == 0 && len(removed) > 0 {
			rex.Notify("Flux · "+c.Short, "all resources healthy again")
		}

		if len(h.failing) > 0 {
			rex.Status(statusID(c), "error", c.Short, fmt.Sprintf("%d Flux resources failing on %s", len(h.failing), c.Short))
		} else {
			rex.Status(statusID(c), "clear", "", "")
		}
	}
}

// diffKeys returns keys present only in cur (added) and only in prev (removed).
func diffKeys[V any](prev, cur map[string]V) (added, removed []string) {
	for k := range cur {
		if _, ok := prev[k]; !ok {
			added = append(added, k)
		}
	}
	for k := range prev {
		if _, ok := cur[k]; !ok {
			removed = append(removed, k)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return
}
