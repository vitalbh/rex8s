package main

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

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
	return a.podAction(cp.C, cl, cp.V, func() {})
}

// pickPodAll: a pod of any cluster (only the failing ones when failing),
// with events and last logs in the preview. When filter matches a single
// pod, that one without asking. Nil when cancelled.
func (a *app) pickPodAll(what string, failing bool, filter string) (*kube.On[kube.Pod], kube.Client, error) {
	cl, err := a.client(a.src)
	if err != nil {
		return nil, cl, err
	}
	res, err := readAll(a, "pods", [][]string{{"get", "pods", "-A", "-o", "json"}},
		func(_ kube.Context, out [][]byte) ([]kube.Pod, error) {
			ps, err := kube.ParsePods(out[0])
			if failing {
				ps = failingPods(ps)
			}
			return ps, err
		})
	if err != nil {
		return nil, cl, err
	}
	all := res.Rows
	printSkipped(res.Skipped)
	if len(all) == 0 {
		if failing {
			return nil, cl, fmt.Errorf("no failing pods on any cluster")
		}
		return nil, cl, fmt.Errorf("no pods on any cluster")
	}
	sort.SliceStable(all, func(i, j int) bool {
		x, y := all[i], all[j]
		if x.C.Short != y.C.Short {
			return x.C.Short < y.C.Short
		}
		if rx, ry := failRank(x.V), failRank(y.V); rx != ry {
			return rx < ry
		}
		if x.V.Namespace != y.V.Namespace {
			return x.V.Namespace < y.V.Namespace
		}
		return x.V.Name < y.V.Name
	})
	if filter != "" {
		var hit []kube.On[kube.Pod]
		for _, cp := range all {
			if matchWords(cp.C.Short+" "+cp.V.Namespace+"/"+cp.V.Name, filter) {
				hit = append(hit, cp)
			}
		}
		if len(hit) == 1 {
			return &hit[0], cl, nil
		}
	}

	wc, wn := 0, 0
	for _, cp := range all {
		wc = max(wc, len(cp.C.Short))
		wn = max(wn, len(cp.V.Namespace)+1+len(cp.V.Name))
	}
	items := make([]tui.Item, len(all))
	for i, cp := range all {
		items[i] = tui.Item{Key: strconv.Itoa(i),
			Label: fmt.Sprintf("%-*s  %-*s", wc, cp.C.Short, wn, cp.V.Namespace+"/"+cp.V.Name),
			Hint:  failHint(cp.V), Badge: cp.V.Badge()}
	}
	at := func(it tui.Item) kube.On[kube.Pod] { i, _ := strconv.Atoi(it.Key); return all[i] }
	it, err := tui.Run(tui.Options{
		Title:       clustersTitle(what, res.OK, len(res.Skipped)),
		Items:       items,
		Query:       filter,
		PreviewHead: func(it tui.Item) string { return failHead(at(it).V) },
		PreviewBody: func(it tui.Item) string { cp := at(it); return failBody(cl, cp.C.Name, cp.V) },
	})
	if err != nil || it == nil {
		return nil, cl, err
	}
	cp := at(*it)
	return &cp, cl, nil
}
