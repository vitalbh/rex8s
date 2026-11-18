package main

import (
	"context"
	"fmt"
	"os"
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
// the ones named) in one stream, each line led by its cluster. A watch that
// ends (API timeout, lost connection) is started again.
func (a *app) kwarnAll(names []string) error {
	clusters, err := a.pickClusters(names)
	if err != nil {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
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
