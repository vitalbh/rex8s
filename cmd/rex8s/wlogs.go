package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/logstream"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// logSpec is one workload's pods to follow: "ctx|ns|selector|label"; its
// text form is a logstream.Spec's, which the log pane reads.
type logSpec struct{ ctx, ns, selector, label string }

func (s logSpec) String() string {
	return logstream.Spec{Ctx: s.ctx, NS: s.ns, Selector: s.selector, Label: s.label}.String()
}

// logOpts: klogs -g / -s.
type logOpts = logstream.Opts

// podSelector marks a logSpec that follows one pod instead of a workload.
const podSelector = logstream.PodSelector

// klogs: pick a workload (all its pods) or a single pod in this namespace and
// follow the logs in a split. klogs -A: workloads on any cluster (mark
// several), merged into one stream.
//
//	-g REGEX   only lines matching REGEX (case-insensitive), matches highlighted
//	-s DUR     start DUR back (e.g. 1h) instead of the last lines
func (a *app) klogs(args []string) error { return a.klogsWith(args, false) }

// kgrep: klogs that searches — kgrep [-A] [PATTERN]: the pattern is asked
// after the pick when not given.
func (a *app) kgrep(args []string) error {
	var rest []string
	pattern := ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-A" || a == "-s" || a == "--since":
			rest = append(rest, a)
			if a != "-A" && i+1 < len(args) {
				rest = append(rest, args[i+1])
				i++
			}
		case pattern == "":
			pattern = a
		default:
			rest = append(rest, a)
		}
	}
	if pattern != "" {
		rest = append(rest, "-g", pattern)
	}
	return a.klogsWith(rest, pattern == "")
}

// askPattern prompts for a regex on the terminal ("" cancels).
func askPattern() string {
	line, _ := tui.AskLine(tui.Bold.Render("Search the logs for") + tui.Dim.Render(" (regex, case-insensitive; empty cancels)") + "\n  > ")
	return line
}

// parseKlogsArgs: klogs [-A] [-g RE] [-s DUR] [FILTER].
func parseKlogsArgs(args []string) (filter string, all bool, lo logOpts, err error) {
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-A":
			all = true
		case (arg == "-g" || arg == "--grep") && i+1 < len(args):
			lo.Grep = args[i+1]
			i++
		case (arg == "-s" || arg == "--since") && i+1 < len(args):
			lo.Since = args[i+1]
			i++
		default:
			filter = arg
		}
	}
	_, err = lo.Compile()
	return filter, all, lo, err
}

func (a *app) klogsWith(args []string, ask bool) error {
	filter, all, lo, err := parseKlogsArgs(args)
	if err != nil {
		return err
	}
	if all {
		return a.klogsClusters(filter, lo, ask)
	}
	c, err := kube.Current(a.cfg, "", "")
	if err != nil {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	lc := newLogChoices(cl, c)
	it, err := tui.Run(tui.Options{
		Title: fmt.Sprintf("Logs · %s · a workload follows all its pods", lc.where),
		Query: filter,
		Load:  lc.load,
	})
	if err != nil || it == nil {
		return err
	}
	if ask {
		if lo.Grep = askPattern(); lo.Grep == "" {
			return nil
		}
		if _, err := lo.Compile(); err != nil {
			return err
		}
	}
	return a.followChoice(c, lc, it.Key, lo)
}

// logChoices is what the klogs picker offers in one cluster: the
// workloads of the namespace (all their pods merged) first, then its pods.
type logChoices struct {
	cl    kube.Client
	c     kube.Context
	scope []string // -n NS, or -A
	where string   // cluster/ns, for titles and errors
	pods  map[string]kube.Pod
	works map[string]target
}

func newLogChoices(cl kube.Client, c kube.Context) *logChoices {
	ns := nsOr(c.Namespace)
	lc := &logChoices{cl: cl, c: c, scope: []string{"-n", ns}, where: c.Short + "/" + ns,
		pods: map[string]kube.Pod{}, works: map[string]target{}}
	if c.AllNS {
		lc.scope, lc.where = []string{"-A"}, c.Short+" (all namespaces)"
	}
	return lc
}

// name of a workload or pod: with every namespace, names carry theirs.
func (lc *logChoices) name(ns, name string) string {
	if lc.c.AllNS {
		return ns + "/" + name
	}
	return name
}

func (lc *logChoices) load() ([]tui.Item, error) {
	var wout, pout []byte
	var werr, perr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		wout, werr = lc.cl.Get(lc.c.Name, append([]string{"get", "deployments,statefulsets,daemonsets", "-o", "json"}, lc.scope...)...)
	}()
	go func() {
		defer wg.Done()
		pout, perr = lc.cl.Get(lc.c.Name, append([]string{"get", "pods", "-o", "json"}, lc.scope...)...)
	}()
	wg.Wait()
	if perr != nil {
		return nil, perr
	}
	ps, err := kube.ParsePods(pout)
	if err != nil {
		return nil, err
	}
	var ws []target
	if werr == nil {
		ws, _ = workloadTargets(wout)
	}
	items := lc.items(ws, ps)
	if len(items) == 0 {
		return nil, fmt.Errorf("no pods in %s", lc.where)
	}
	return items, nil
}

// items: the workloads, then the pods, broken first.
func (lc *logChoices) items(ws []target, ps []kube.Pod) []tui.Item {
	w := 0
	for _, t := range ws {
		w = max(w, len(kube.ShortKind(t.Kind))+1+len(lc.name(t.Namespace, t.Name)))
	}
	for _, p := range ps {
		w = max(w, len(lc.name(p.Namespace, p.Name)))
	}
	var items []tui.Item
	for _, t := range ws {
		if t.Selector == "" {
			continue
		}
		n := 0
		for _, p := range ps {
			if p.Namespace == t.Namespace && matchesSelector(p, t) {
				n++
			}
		}
		key := "w:" + t.Kind + "/" + t.Namespace + "/" + t.Name
		lc.works[key] = t
		items = append(items, tui.Item{Key: key, Label: fmt.Sprintf("%-*s", w, kube.ShortKind(t.Kind)+" "+lc.name(t.Namespace, t.Name)),
			Hint: fmt.Sprintf("all %d pod(s), merged", n), Badge: "ALL"})
	}
	rank := map[string]int{"FAIL": 0, "WAIT": 1, "ok": 2, "done": 3}
	sort.SliceStable(ps, func(i, j int) bool { return rank[ps[i].Badge()] < rank[ps[j].Badge()] })
	for _, p := range ps {
		name := lc.name(p.Namespace, p.Name)
		lc.pods[name] = p
		items = append(items, tui.Item{Key: name, Label: fmt.Sprintf("%-*s", w, name), Badge: p.Badge(),
			Hint: fmt.Sprintf("%-18s %d/%d  restarts %-3d %s", p.Status(), p.Ready, p.Total, p.Restarts, kube.Age(p.Created))})
	}
	return items
}

// followChoice follows the picked workload or pod: in a split behind this
// pane in Rex, here outside it.
func (a *app) followChoice(c kube.Context, lc *logChoices, key string, lo logOpts) error {
	self := filepath.Join(a.binDir, "rex8s")
	if t, ok := lc.works[key]; ok {
		spec := logSpec{c.Name, t.Namespace, t.Selector, kube.ShortKind(t.Kind) + " " + t.Name}
		if rex.Block() == "" {
			return a.wlogtail([]string{spec.String()}, lo)
		}
		return rex.SplitBehind("logs "+t.Name, append([]string{self, "_wlogs"}, append(lo.Flags(), spec.String())...)...)
	}
	p := lc.pods[key]
	container, ok, err := pickContainer(p, true)
	if err != nil || !ok {
		return err
	}
	if rex.Block() == "" {
		return a.logtail(c.Name, p.Namespace, p.Name, container, lo)
	}
	argv := append([]string{self, "_logs"}, lo.Flags()...)
	argv = append(argv, c.Name, p.Namespace, p.Name)
	if container != "" {
		argv = append(argv, container)
	}
	return rex.SplitBehind("logs "+p.Name, argv...)
}

// matchesSelector: the pod carries the workload's matchLabels. The pod list
// has no labels, so this asks by name prefix (pods are named after their
// workload) — only used for the count shown in the picker.
func matchesSelector(p kube.Pod, t target) bool {
	return strings.HasPrefix(p.Name, t.Name+"-")
}

// klogsClusters: klogs -A — workloads and pods of every cluster in one
// picker; mark any mix (space) and they follow in one stream, each line led
// by its source.
func (a *app) klogsClusters(filter string, lo logOpts, ask bool) error {
	type source struct {
		c    kube.Context
		t    *target   // a workload, or
		p    *kube.Pod // a single pod
		name string    // ns/name
	}
	res, err := readAll(a, "workloads and pods", [][]string{
		{"get", "deployments,statefulsets,daemonsets", "-A", "-o", "json"},
		{"get", "pods", "-A", "-o", "json"},
	}, func(c kube.Context, out [][]byte) ([]source, error) {
		var srcs []source
		ws, _ := workloadTargets(out[0])
		for _, t := range ws {
			if t.Selector != "" {
				t.c = c
				srcs = append(srcs, source{c: c, t: &t, name: t.Namespace + "/" + t.Name})
			}
		}
		ps, err := kube.ParsePods(out[1])
		if err != nil {
			return nil, err
		}
		for _, p := range ps {
			srcs = append(srcs, source{c: c, p: &p, name: p.Namespace + "/" + p.Name})
		}
		return srcs, nil
	})
	if err != nil {
		return err
	}
	srcs := make([]source, len(res.Rows))
	for i, r := range res.Rows {
		srcs[i] = r.V
	}
	printSkipped(res.Skipped)
	if len(srcs) == 0 {
		return fmt.Errorf("no workloads or pods on any cluster")
	}
	// per cluster: workloads, then pods (failing first)
	rank := map[string]int{"FAIL": 0, "WAIT": 1, "ok": 2, "done": 3}
	sort.SliceStable(srcs, func(i, j int) bool {
		x, y := srcs[i], srcs[j]
		if x.c.Short != y.c.Short {
			return x.c.Short < y.c.Short
		}
		if (x.t != nil) != (y.t != nil) {
			return x.t != nil
		}
		if x.p != nil && y.p != nil && rank[x.p.Badge()] != rank[y.p.Badge()] {
			return rank[x.p.Badge()] < rank[y.p.Badge()]
		}
		return x.name < y.name
	})
	wc, wn := 0, 0
	for _, s := range srcs {
		wc, wn = max(wc, len(s.c.Short)), max(wn, len(s.name))
	}
	items := make([]tui.Item, len(srcs))
	for i, s := range srcs {
		it := tui.Item{Key: strconv.Itoa(i)}
		if s.t != nil {
			it.Label = fmt.Sprintf("%-*s  %-6s %-*s", wc, s.c.Short, kube.ShortKind(s.t.Kind), wn, s.name)
			it.Hint, it.Badge = "all its pods, merged", "ALL"
		} else {
			p := s.p
			it.Label = fmt.Sprintf("%-*s  %-6s %-*s", wc, s.c.Short, "pod", wn, s.name)
			it.Hint = fmt.Sprintf("%-18s %d/%d  restarts %-3d %s", p.Status(), p.Ready, p.Total, p.Restarts, kube.Age(p.Created))
			it.Badge = p.Badge()
		}
		items[i] = it
	}
	chosen, err := tui.RunMulti(tui.Options{
		Title: clustersTitle("Logs · space marks several (workloads and pods), one stream", res.OK, len(res.Skipped)),
		Items: items, Query: filter})
	if err != nil || len(chosen) == 0 {
		return err
	}
	if ask {
		if lo.Grep = askPattern(); lo.Grep == "" {
			return nil
		}
		if _, err := lo.Compile(); err != nil {
			return err
		}
	}
	var specs, names []string
	for _, it := range chosen {
		i, _ := strconv.Atoi(it.Key)
		s := srcs[i]
		if s.t != nil {
			specs = append(specs, logSpec{s.c.Name, s.t.Namespace, s.t.Selector, s.c.Short + " " + s.t.Name}.String())
			names = append(names, s.t.Name)
		} else {
			specs = append(specs, logSpec{s.c.Name, s.p.Namespace, podSelector + s.p.Name, s.c.Short + " " + s.p.Name}.String())
			names = append(names, s.p.Name)
		}
	}
	if rex.Block() == "" {
		return a.wlogtail(specs, lo)
	}
	label := "logs " + names[0]
	if len(names) > 1 {
		label = fmt.Sprintf("logs %s +%d", names[0], len(names)-1)
	}
	argv := append([]string{filepath.Join(a.binDir, "rex8s"), "_wlogs"}, lo.Flags()...)
	return rex.SplitBehind(label, append(argv, specs...)...)
}

// wlogtail follows every pod of one or more workloads, one merged stream
// (runs in its own pane). When the set of pods changes (a rollout), the
// stream is restarted so the new pods show up.
func (a *app) wlogtail(raw []string, lo logOpts) error {
	re, err := lo.Compile()
	if err != nil {
		return err
	}
	var specs []logstream.Spec
	for _, r := range raw {
		if s, ok := logstream.ParseSpec(r); ok {
			specs = append(specs, s)
		}
	}
	if len(specs) == 0 {
		return fmt.Errorf("_wlogs: nothing to follow")
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	cl.Timeout = 30 * time.Second
	var labels []string
	clusters := map[string]kube.Context{}
	prod := false
	for _, s := range specs {
		labels = append(labels, s.Label)
		if c, err := a.context(s.Ctx); err == nil {
			clusters[s.Ctx] = c
			prod = prod || c.Prod
		}
	}
	rex.Rename("logs " + strings.Join(labels, ", "))
	if c, ok := clusters[specs[0].Ctx]; ok {
		if prod {
			c.Prod = true
		}
		a.tint(c)
	}
	fmt.Println(tui.Dim.Render("following " + strings.Join(labels, " · ") + lo.Describe() + " · new pods join after a rollout · ctrl+c to stop"))

	tab := newTabStatus()
	defer tab.Clear()
	f := &logstream.Follower{Client: cl, Clusters: clusters, Links: a.logLinks, Grep: re, Since: lo.Since,
		State: newLogStatus(tab, lo.Grep).update}
	var wg sync.WaitGroup
	for i, s := range specs {
		label := logstream.StreamLabel(s.Label, logstream.StreamColors[i%len(logstream.StreamColors)], len(specs) > 1)
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Follow(context.Background(), s, label)
		}()
	}
	wg.Wait()
	waitClose("logs ended")
	return nil
}

// logLinks is the log streams' Linker maker: the forward.Linker of the
// stream's cluster and namespace, or nil (no links: not a terminal, or
// log_links: off).
func (a *app) logLinks(ctx, ns string) logstream.Linker {
	if l := a.newLogLinker(ctx, ns); l != nil {
		return l
	}
	return nil
}
