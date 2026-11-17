package main

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

type clusterPod struct {
	c kube.Context
	p pod
}

// kpods: the pods of every cluster in one picker — per cluster the ones that
// need attention first, then by namespace and name. `kpods failing` keeps
// only those (kfail on every cluster); any other word is the initial filter.
// Preview: events and last log lines. Enter: logs / why / ask / shell /
// describe / events, as in kfail.
func (a *app) kpods(args []string) error {
	failing, filter := false, ""
	for _, arg := range args {
		if arg == "failing" || arg == "-f" {
			failing = true
		} else {
			filter = arg
		}
	}
	what := "Pods"
	if failing {
		what = "Failing pods"
	}
	cp, cl, err := a.pickPodAll(what, failing, filter)
	if err != nil || cp == nil {
		return err
	}
	return a.podAction(cp.c, cl, cp.p, func() {})
}

// pickPodAll: a pod of any cluster (only the failing ones when failing),
// with events and last logs in the preview. When filter matches a single
// pod, that one without asking. Nil when cancelled.
func (a *app) pickPodAll(what string, failing bool, filter string) (*clusterPod, kube.Client, error) {
	cl, err := a.client(a.src)
	if err != nil {
		return nil, cl, err
	}
	reads, skipped, err := a.readAllClusters("pods", []string{"get", "pods", "-A", "-o", "json"})
	if err != nil {
		return nil, cl, err
	}
	var all []clusterPod
	for _, r := range reads {
		ps, err := parsePods(r.out[0])
		if err != nil {
			skipped = append(skipped, r.c.Short+": "+err.Error())
			continue
		}
		if failing {
			ps = failingPods(ps)
		}
		for _, p := range ps {
			all = append(all, clusterPod{r.c, p})
		}
	}
	printSkipped(skipped)
	if len(all) == 0 {
		if failing {
			return nil, cl, fmt.Errorf("no failing pods on any cluster")
		}
		return nil, cl, fmt.Errorf("no pods on any cluster")
	}
	sort.SliceStable(all, func(i, j int) bool {
		x, y := all[i], all[j]
		if x.c.Short != y.c.Short {
			return x.c.Short < y.c.Short
		}
		if rx, ry := failRank(x.p), failRank(y.p); rx != ry {
			return rx < ry
		}
		if x.p.Namespace != y.p.Namespace {
			return x.p.Namespace < y.p.Namespace
		}
		return x.p.Name < y.p.Name
	})
	if filter != "" {
		var hit []clusterPod
		for _, cp := range all {
			if matchWords(cp.c.Short+" "+cp.p.Namespace+"/"+cp.p.Name, filter) {
				hit = append(hit, cp)
			}
		}
		if len(hit) == 1 {
			return &hit[0], cl, nil
		}
	}

	wc, wn := 0, 0
	for _, cp := range all {
		wc = max(wc, len(cp.c.Short))
		wn = max(wn, len(cp.p.Namespace)+1+len(cp.p.Name))
	}
	items := make([]tui.Item, len(all))
	for i, cp := range all {
		items[i] = tui.Item{Key: strconv.Itoa(i),
			Label: fmt.Sprintf("%-*s  %-*s", wc, cp.c.Short, wn, cp.p.Namespace+"/"+cp.p.Name),
			Hint:  failHint(cp.p), Badge: cp.p.badge()}
	}
	at := func(it tui.Item) clusterPod { i, _ := strconv.Atoi(it.Key); return all[i] }
	it, err := tui.Run(tui.Options{
		Title:       clustersTitle(what, len(reads), skipped),
		Items:       items,
		Query:       filter,
		PreviewHead: func(it tui.Item) string { return failHead(at(it).p) },
		PreviewBody: func(it tui.Item) string { cp := at(it); return failBody(cl, cp.c.Name, cp.p) },
	})
	if err != nil || it == nil {
		return nil, cl, err
	}
	cp := at(*it)
	return &cp, cl, nil
}
