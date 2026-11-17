package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// clusterRead is what one `kubectl get` returned on one cluster.
type clusterRead struct {
	c   kube.Context
	out [][]byte // one per query, in order
}

// readAllClusters runs the queries on every cluster at once and returns the
// clusters that answered every query, plus a "SHORT: why" line for each
// cluster that did not (login expired, unreachable…). what names the
// objects for the progress line ("services", "pods").
func (a *app) readAllClusters(what string, queries ...[]string) ([]clusterRead, []string, error) {
	cl, err := a.client(a.src)
	if err != nil {
		return nil, nil, err
	}
	cl.Timeout = 20 * time.Second
	raw, err := kube.Load(a.src)
	if err != nil {
		return nil, nil, err
	}
	clusters := uniqueClusters(kube.Contexts(raw, a.cfg))
	fmt.Fprintln(os.Stderr, tui.Dim.Render(fmt.Sprintf("reading %s on %d clusters…", what, len(clusters))))
	defer fmt.Fprint(os.Stderr, "\033[1A\033[2K")

	type res struct {
		out [][]byte
		err error
	}
	results := make([]res, len(clusters))
	var wg sync.WaitGroup
	for i, c := range clusters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, q := range queries {
				out, err := cl.Get(c.Name, q...)
				if err != nil {
					results[i].err = err
					return
				}
				results[i].out = append(results[i].out, out)
			}
		}()
	}
	wg.Wait()

	var ok []clusterRead
	var skipped []string
	for i, r := range results {
		c := clusters[i]
		if r.err != nil {
			why := lastLogLine(r.err.Error())
			if kube.AuthExpired(r.err) {
				why = "login expired (kauth " + c.Short + ")"
			}
			skipped = append(skipped, c.Short+": "+why)
			continue
		}
		ok = append(ok, clusterRead{c, r.out})
	}
	return ok, skipped, nil
}

// clustersTitle: "Pods on 4 clusters (1 skipped)".
func clustersTitle(what string, ok int, skipped []string) string {
	t := fmt.Sprintf("%s on %d clusters", what, ok)
	if len(skipped) > 0 {
		t += fmt.Sprintf(" (%d skipped)", len(skipped))
	}
	return t
}

func printSkipped(skipped []string) {
	for _, s := range skipped {
		fmt.Fprintln(os.Stderr, tui.Dim.Render("skipped "+s))
	}
}
