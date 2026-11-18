package main

import (
	"fmt"
	"os"
	"time"

	"github.com/vitalbh/rex8s/internal/demo"
	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
	"golang.org/x/term"
)

// clusterProbe is how long a multi-cluster read waits for a TCP connection
// to a cluster's API server before skipping it as unreachable.
const clusterProbe = 3 * time.Second

// reader is a fleet.Reader for multi-cluster reads, 20s per query.
func (a *app) reader() (*fleet.Reader, error) {
	cl, err := a.client(a.src)
	if err != nil {
		return nil, err
	}
	cl.Timeout = 20 * time.Second
	return a.readerFor(cl), nil
}

// readerFor is a fleet.Reader that reads with cl and probes each cluster's
// API server first (not in demo mode, whose servers do not exist).
func (a *app) readerFor(cl kube.Client) *fleet.Reader {
	r := &fleet.Reader{Client: cl}
	if !demo.Active() {
		r.Probe = clusterProbe
	}
	return r
}

// readAll runs the queries on every cluster at once, with a "reading WHAT
// on N clusters…" line on stderr while it runs.
func readAll[T any](a *app, what string, queries [][]string, parse fleet.Parse[T]) (fleet.Result[T], error) {
	r, err := a.reader()
	if err != nil {
		return fleet.Result[T]{}, err
	}
	raw, err := kube.Load(a.src)
	if err != nil {
		return fleet.Result[T]{}, err
	}
	clusters := fleet.All(raw, a.cfg)
	fmt.Fprintln(os.Stderr, tui.Dim.Render(fmt.Sprintf("reading %s on %d clusters…", what, len(clusters))))
	if term.IsTerminal(int(os.Stderr.Fd())) { // erase the line; not in a redirected stderr
		defer fmt.Fprint(os.Stderr, "\033[1A\033[2K")
	}
	return fleet.Read(r, clusters, queries, parse), nil
}

// clustersTitle: "Pods on 4 clusters (1 skipped)".
func clustersTitle(what string, ok, skipped int) string {
	t := fmt.Sprintf("%s on %d clusters", what, ok)
	if skipped > 0 {
		t += fmt.Sprintf(" (%d skipped)", skipped)
	}
	return t
}

func printSkipped(skipped []fleet.Skip) {
	for _, s := range skipped {
		fmt.Fprintln(os.Stderr, tui.Dim.Render("skipped "+s.String()))
	}
}
