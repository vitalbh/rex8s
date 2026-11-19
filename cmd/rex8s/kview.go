package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/kwatch"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kview: a k9s-like browser of this pane's cluster (every namespace), or of
// every cluster with -A (or of a group or the clusters named). Tabs: pods,
// workloads, services, jobs, nodes, warnings — each refreshes by itself —
// and ":KIND" opens one for any other kind (kview_resource.go). Letters act
// on the row under the cursor, or on every marked row (kview_actions.go):
// logs and shells open in Rex splits, describe and YAML in a viewer, changes
// (delete, restart, run a CronJob) go through the production guard.
//
//	kview [-A [GROUP | CLUSTER[,CLUSTER…]] | --context CTX] [pods|workloads|services|jobs|nodes|warnings]
type kview struct {
	a     *app
	cl    kube.Client
	fleet *fleet.Reader // remembers unreachable clusters between refreshes
	// hub watches the tabs' objects (nil: kubectl on every refresh, as
	// in demo mode and tests); wakes is each tab's View.Wake.
	hub      *kwatch.Hub
	wakeMu   sync.Mutex // wakes and queries, and the Notify calls with them
	wakes    map[string]chan struct{}
	queries  map[string][][]string // view → what its loader reads (for View.Live)
	clusters []kube.Context
	multi    bool

	mu      sync.Mutex
	objs    map[string]any    // row Key → what it shows (pod, workload, …), from the last load
	headers map[string]string // view → its column titles, sized by the last load

	metrics *kvMetrics // CPU and memory (nil: no columns filled, as in tests)
	objects kvObjects  // what the watches hold, for y (the hub; nil: kubectl)
	next    kvNextRuns // the CronJobs' next runs, for the jobs tab
	splits  kvSplits   // the Rex splits it opened
	api     kvAPI      // kubectl api-resources, for ":"

	// tab is the status on the Rex tab (nil in tests): failing pods,
	// expired logins.
	tab     *tabStatus
	stMu    sync.Mutex
	podsIn  bool // the pods tab loaded: failing counts
	failing int
	expired map[string]bool // cluster short → its login expired in the last load

	out   io.Writer // where changes on many objects print (the terminal)
	pause func()    // waits for enter after them
}

func (k *kview) setHeader(view, h string) {
	k.mu.Lock()
	k.headers[view] = h
	k.mu.Unlock()
}

// header: the view's column titles, with the cluster column under -A.
func (k *kview) header(view, fallback string) func() string {
	return func() string {
		k.mu.Lock()
		h, ok := k.headers[view]
		k.mu.Unlock()
		if !ok {
			h = fallback
		}
		if k.multi {
			return fmt.Sprintf("%-*s  %s", k.clusterWidth(), "CLUSTER", h)
		}
		return h
	}
}

const (
	vPods = iota
	vWorkloads
	vServices
	vJobs
	vNodes
	vWarnings
	vResource // the tab ":KIND" opened
)

var kviewNames = []string{"pods", "workloads", "services", "jobs", "nodes", "warnings"}

// kviewOpts is what kview opens on.
type kviewOpts struct {
	all      bool     // -A: every cluster…
	clusters []string // …or these (names, short names or groups; "" none)
	ctxName  string   // --context ("" with all, or for the pane's cluster)
	start    int      // the first tab
	filter   string   // its filter
	ns       string   // the namespace the pane is on, for the pane label ("" none)
}

// kviewTabArg is the tab an argument names ("svc", "deploy", a prefix), or -1.
func kviewTabArg(arg string) int {
	arg = strings.ToLower(arg)
	switch arg {
	case "":
		return -1
	case "svc":
		return vServices
	case "deploy":
		return vWorkloads
	}
	for i, n := range kviewNames {
		if strings.HasPrefix(n, arg) {
			return i
		}
	}
	return -1
}

func parseKviewArgs(args []string) (kviewOpts, error) {
	var o kviewOpts
	usage := fmt.Errorf("kview [-A [GROUP | CLUSTER[,CLUSTER…]] | --context CTX] [%s]", strings.Join(kviewNames, "|"))
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-A":
			o.all = true
		case arg == "--context" && i+1 < len(args):
			o.ctxName = args[i+1]
			i++
		case arg == "--filter" && i+1 < len(args): // the first tab starts filtered (⇧⌘K on a namespace)
			o.filter = args[i+1]
			i++
		case kviewTabArg(arg) >= 0 && !strings.Contains(arg, ","):
			o.start = kviewTabArg(arg)
		case o.all && !strings.HasPrefix(arg, "-"): // kview -A use,euw · kview -A prod
			for _, n := range strings.Split(arg, ",") {
				if n = strings.TrimSpace(n); n != "" {
					o.clusters = append(o.clusters, n)
				}
			}
		default:
			return o, usage
		}
	}
	if o.all && o.ctxName != "" {
		return o, usage
	}
	return o, nil
}

func (a *app) kview(args []string) error {
	o, err := parseKviewArgs(args)
	if err != nil {
		return err
	}
	return a.runKview(o)
}

// kviewClusters: every cluster, or the ones names means — each a context,
// a short name or a group of the config (groups: {prod: [use, euw]}) —
// each once, in the order given.
func (a *app) kviewClusters(names []string) ([]kube.Context, error) {
	if len(names) == 0 {
		return a.pickClusters(nil)
	}
	var expanded []string
	for _, n := range names {
		if g, ok := a.cfg.Groups[n]; ok {
			if len(g) == 0 {
				return nil, fmt.Errorf("group %s has no clusters", n)
			}
			expanded = append(expanded, g...)
			continue
		}
		expanded = append(expanded, n)
	}
	cs, err := a.pickClusters(expanded)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := cs[:0]
	for _, c := range cs {
		if !seen[c.Name] {
			seen[c.Name] = true
			out = append(out, c)
		}
	}
	return out, nil
}

// kviewLabel is the pane label: "kview PROD use/payments", "kview euw",
// "kview PROD all" (-A with a production cluster in it), "kview PROD
// all:use,euw" (-A use,euw; scope is what -A was given). rex8s.lua's ⇧⌘I
// toggle reads it back: keep the two in step.
func kviewLabel(clusters []kube.Context, all bool, scope, ns string) string {
	if all {
		prod := ""
		for _, c := range clusters {
			if c.Prod {
				prod = "PROD "
			}
		}
		if scope != "" {
			return "kview " + prod + "all:" + scope
		}
		return "kview " + prod + "all"
	}
	c := clusters[0]
	l := c.Short
	if ns != "" {
		l = strings.TrimPrefix(labelText(c, ns), "PROD ")
		l = strings.TrimPrefix(l, "k8s ")
	}
	if c.Prod {
		return "kview PROD " + l
	}
	return "kview " + l
}

func (a *app) runKview(o kviewOpts) error {
	r, err := a.reader()
	if err != nil {
		return err
	}
	k := newKview(a, r)
	title := ""
	if o.all {
		if k.clusters, err = a.kviewClusters(o.clusters); err != nil {
			return err
		}
		if len(k.clusters) == 0 {
			return errors.New("no clusters")
		}
		k.multi, title = true, fmt.Sprintf("kview · %d clusters", len(k.clusters))
		if len(o.clusters) > 0 {
			title = "kview · " + strings.Join(o.clusters, ",")
		}
	} else {
		var c kube.Context
		if o.ctxName != "" {
			c, err = a.context(o.ctxName)
		} else {
			c, err = kube.Current(a.cfg, "", "")
		}
		if err != nil {
			return err
		}
		k.clusters, title = []kube.Context{c}, "kview · "+c.Short
		if c.Prod {
			title += " PROD"
		}
		a.tint(c)
	}
	k.metrics = newKvMetrics(k.cl)
	k.tab = newTabStatus()
	var stopHub func()
	k.hub, stopHub = a.watchHub(k.cl, k.fleet)
	defer stopHub()
	if k.hub != nil {
		k.metrics.api, k.objects = k.hub, k.hub
	}
	rex.Rename(kviewLabel(k.clusters, o.all, strings.Join(o.clusters, ","), o.ns))

	// closing the pane (SIGHUP) still closes the splits it opened
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		if _, ok := <-hup; ok {
			k.cleanup()
			os.Exit(1)
		}
	}()
	defer func() {
		signal.Stop(hup)
		close(hup)
		k.cleanup()
	}()
	return tui.RunBrowser(tui.BrowserOptions{Title: title, Views: k.views(), Start: o.start, Filter: o.filter,
		Action: k.action, ActionMany: k.actionMany, Command: k.command, Complete: k.complete, Reload: k.fleet.Retry})
}

func newKview(a *app, r *fleet.Reader) *kview {
	return &kview{a: a, cl: r.Client, fleet: r, objs: map[string]any{}, headers: map[string]string{}, expired: map[string]bool{},
		out: os.Stdout, pause: pressEnter}
}

// cleanup: what kview leaves behind in Rex goes with it — its splits and
// its status on the tab.
func (k *kview) cleanup() {
	k.splits.closeAll()
	if k.tab != nil {
		k.tab.Clear()
	}
}

// wake makes the View.Wake of a tab: nil without a watch.
func (k *kview) wake(view string) <-chan struct{} {
	if k.hub == nil {
		return nil
	}
	k.wakeMu.Lock()
	defer k.wakeMu.Unlock()
	if k.wakes == nil {
		k.wakes = map[string]chan struct{}{}
	}
	ch := make(chan struct{}, 1)
	k.wakes[view] = ch
	return ch
}

// notify asks the hub to wake the tab when what its queries read changes
// (nothing once its wake was dropped).
func (k *kview) notify(view string, queries [][]string) {
	k.wakeMu.Lock()
	defer k.wakeMu.Unlock()
	ch := k.wakes[view]
	if ch == nil {
		return
	}
	for _, c := range k.clusters {
		for _, q := range queries {
			k.hub.Notify(c, q, ch)
		}
	}
}

// dropWakes ends the wakes of the tabs whose view starts with prefix (a
// ":" tab another replaced): their watches stop, and their channels close,
// which ends the browser's wait on them.
func (k *kview) dropWakes(prefix string) {
	k.wakeMu.Lock()
	defer k.wakeMu.Unlock()
	for view, ch := range k.wakes {
		if strings.HasPrefix(view, prefix) {
			k.hub.Release(ch) // nothing sends on ch once it returns
			close(ch)
			delete(k.wakes, view)
		}
	}
}

// live is a tab's View.Live: "live" when watches answer for every cluster,
// "live · polling sa,euw" when kubectl reads some (a legacy auth-provider,
// a kind a cluster doesn't have), "" when it reads them all.
func (k *kview) live(view string) func() string {
	if k.hub == nil {
		return nil
	}
	return func() string {
		k.wakeMu.Lock()
		queries := k.queries[view]
		k.wakeMu.Unlock()
		var polling []string
		for _, c := range k.clusters {
			for _, q := range queries {
				if !k.hub.Watching(c.Name, q) {
					polling = append(polling, c.Short)
					break
				}
			}
		}
		switch {
		case len(polling) == 0:
			return "live"
		case len(polling) == len(k.clusters):
			return ""
		case len(polling) <= 3:
			return "live · polling " + strings.Join(polling, ",")
		}
		return fmt.Sprintf("live · %d polling", len(polling))
	}
}

func (k *kview) views() []tui.View {
	every := func(one, many time.Duration) time.Duration {
		if k.multi {
			return many
		}
		return one
	}
	vs := []tui.View{
		{Name: "pods", Wake: k.wake("pods"), Header: k.header("pods", podHeader), Load: podTab.loader(k), Every: every(5*time.Second, 15*time.Second),
			Actions: []tui.Action{act("enter", "owner"), act("l", "logs"), act("p", "previous logs"), act("s", "shell"), act("d", "describe"),
				act("e", "events"), act("y", "yaml"), act("w", "why"), act("a", "ask"), many("ctrl+d", "delete")},
			Sorts: k.sorts(sortName, sortAge, sortStatus, sortRestarts, sortCPU, sortMem)},
		{Name: "workloads", Wake: k.wake("workloads"), Header: k.header("workloads", workloadHeader), Load: workloadTab.loader(k), Every: every(10*time.Second, 20*time.Second),
			Actions: []tui.Action{act("enter", "pods"), act("l", "logs"), many("r", "restart"), act("d", "describe"), act("y", "yaml")},
			Sorts:   k.sorts(sortName, sortAge, sortStatus)},
		{Name: "services", Wake: k.wake("services"), Header: k.header("services", serviceHeader), Load: serviceTab.loader(k), Every: 30 * time.Second,
			Actions: []tui.Action{act("enter", "forward"), act("o", "pods"), act("d", "describe"), act("y", "yaml")},
			Sorts:   k.sorts(sortName)},
		{Name: "jobs", Wake: k.wake("jobs"), Header: k.header("jobs", jobHeader), Load: jobTab.loader(k), Every: every(15*time.Second, 30*time.Second),
			Actions: []tui.Action{act("enter", "runs/pods"), act("l", "logs"), danger("r", "run now"), act("d", "describe"), act("y", "yaml")},
			Sorts:   k.sorts(sortName)},
		{Name: "nodes", Wake: k.wake("nodes"), Header: k.header("nodes", nodeHeader), Load: nodeTab.loader(k), Every: 30 * time.Second,
			Actions: []tui.Action{act("enter", "pods"), act("s", "shell"), act("d", "describe"), act("y", "yaml")},
			Sorts:   k.sorts(sortName, sortAge, sortStatus, sortCPU, sortMem)},
		{Name: "warnings", Wake: k.wake("warnings"), Header: k.header("warnings", warningHeader), Load: warningTab.loader(k), Every: every(10*time.Second, 20*time.Second),
			Actions: []tui.Action{act("enter", "go to object"), act("d", "describe")}},
	}
	for i := range vs {
		vs[i].Live = k.live(vs[i].Name)
	}
	return vs
}

func act(key, label string) tui.Action { return tui.Action{Key: key, Label: label} }

// danger: an action that changes the cluster — y/N in the browser first.
func danger(key, label string) tui.Action { return tui.Action{Key: key, Label: label, Confirm: true} }

// many: a danger that also takes every marked row (one y/N listing them).
func many(key, label string) tui.Action {
	return tui.Action{Key: key, Label: label, Confirm: true, Multi: true}
}

func (k *kview) clusterWidth() int {
	w := len("CLUSTER")
	for _, c := range k.clusters {
		w = max(w, len(c.Short))
	}
	return w
}

// row: the cluster column (with -A, production in red), then the cells,
// cleaned of anything a cluster could put in them to drive the terminal.
func (k *kview) row(c kube.Context, cells string) string {
	cells = tui.Clean(cells)
	if k.multi {
		name := fmt.Sprintf("%-*s", k.clusterWidth(), c.Short)
		if c.Prod { // production in red, like its panes
			name = tui.Err.Render(name)
		}
		return name + "  " + cells
	}
	return cells
}

// kvName is what a row is in prompts: "pod default/web-7d9f on use (PROD)".
func kvName(c kube.Context, kind, ref string) string {
	s := kind + " " + ref + " on " + c.Short
	if c.Prod {
		s += " (PROD)"
	}
	return s
}

func (k *kview) keep(objs map[string]any, prefix string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for key := range k.objs {
		if strings.HasPrefix(key, prefix) {
			delete(k.objs, key)
		}
	}
	for key, o := range objs {
		k.objs[key] = o
	}
}

func (k *kview) obj(key string) (any, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	o, ok := k.objs[key]
	return o, ok
}

// ── the loader ─────────────────────────────────────────────────────────

// kviewTab is how a tab reads its objects and turns them into rows.
type kviewTab[T any] struct {
	view    string // the tab's name (its header's key)
	prefix  string // its rows' Key prefix: "pod|"
	queries [][]string
	parse   fleet.Parse[T]
	// rows makes the rows of what was read, every cluster together: the
	// column titles, the rows, and the object behind each row by Key.
	rows func(k *kview, read []kube.On[T]) (header string, items []tui.Item, objs map[string]any)
	// metrics: the tab shows CPU and memory of this kind ("pods", "nodes").
	metrics string
}

// parseOne parses a single query's answer.
func parseOne[T any](parse func([]byte) ([]T, error)) fleet.Parse[T] {
	return func(_ kube.Context, out [][]byte) ([]T, error) { return parse(out[0]) }
}

// loader is the tab's View.Load. Under -A each cluster shows as it answers:
// until it does, its rows from the previous load stay. Clusters skipped
// (unreachable, backed off, login expired) come last as dim rows; when every
// cluster is skipped the load fails, and the browser keeps the last rows.
func (t kviewTab[T]) loader(k *kview) func(partial func([]tui.Item)) ([]tui.Item, error) {
	k.wakeMu.Lock()
	if k.queries == nil {
		k.queries = map[string][][]string{}
	}
	k.queries[t.view] = t.queries
	k.wakeMu.Unlock()
	var prev map[string][]kube.On[T] // cluster name → its rows from the last load
	return func(partial func([]tui.Item)) ([]tui.Item, error) {
		var mu sync.Mutex
		fresh, skips := map[string][]kube.On[T]{}, map[string]fleet.Skip{}
		build := func(final bool) []tui.Item {
			var read []kube.On[T]
			var tail []tui.Item
			for _, c := range k.clusters {
				if s, ok := skips[c.Name]; ok {
					tail = append(tail, skipRow(s))
					continue
				}
				rows, ok := fresh[c.Name]
				if !ok && !final {
					rows = prev[c.Name]
				}
				read = append(read, rows...)
			}
			header, items, objs := t.rows(k, read)
			if header != "" {
				k.setHeader(t.view, header)
			}
			k.keep(objs, t.prefix)
			return append(items, tail...)
		}
		k.notify(t.view, t.queries) // changes wake the tab: ask before reading, to miss none
		res := fleet.ReadEach(k.fleet, k.clusters, t.queries, t.parse, func(c kube.Context, rows []kube.On[T], skip *fleet.Skip) {
			if skip == nil && t.metrics != "" && k.metrics != nil {
				k.metrics.refresh(c, t.metrics) // in the background, at most every metricsEvery
			}
			mu.Lock()
			defer mu.Unlock()
			if skip != nil {
				skips[c.Name] = *skip
			} else {
				fresh[c.Name] = rows
			}
			if k.multi && partial != nil {
				partial(build(false))
			}
		})
		k.noteSkips(res.Skipped, len(res.Skipped) < len(k.clusters))
		if res.OK == 0 && len(res.Skipped) > 0 {
			return nil, errors.New(strings.Join(res.Strings(), " · "))
		}
		mu.Lock()
		defer mu.Unlock()
		prev = fresh
		items := build(true)
		if t.view == "pods" {
			k.noteFailing(items)
		}
		return items, nil
	}
}

// skipRow is a dim row for a cluster a load left out: "skipped use:
// unreachable (…) (since 14:02, next try 14:03:30)".
func skipRow(s fleet.Skip) tui.Item {
	it := tui.Item{Label: "skipped " + s.String(), Dim: true}
	if !s.Retry.IsZero() {
		it.Hint = "R tries now"
	}
	return it
}

// ── the Rex tab's status ───────────────────────────────────────────────

// noteSkips records which clusters' logins expired in a load (some of the
// clusters answered: the others are fine).
func (k *kview) noteSkips(skips []fleet.Skip, someAnswered bool) {
	k.stMu.Lock()
	expired := map[string]bool{}
	for _, s := range skips {
		if s.Expired {
			expired[s.Cluster.Short] = true
			k.a.offerLogin(s.Cluster)
		}
	}
	if someAnswered || len(expired) > 0 {
		k.expired = expired
	}
	k.stMu.Unlock()
	k.report()
}

// noteFailing counts the pods that fail (FAIL rows) after a pods load.
func (k *kview) noteFailing(items []tui.Item) {
	n := 0
	for _, it := range items {
		if it.Key != "" && it.Badge == "FAIL" {
			n++
		}
	}
	k.stMu.Lock()
	k.failing, k.podsIn = n, true
	k.stMu.Unlock()
	k.report()
}

// statusText is the short status of the Rex tab: "3 failing · login expired:
// sa" ("" when all is well).
func (k *kview) statusText() string {
	var parts []string
	if k.failing > 0 {
		parts = append(parts, fmt.Sprintf("%d failing", k.failing))
	}
	if len(k.expired) > 0 {
		var names []string
		for n := range k.expired {
			names = append(names, n)
		}
		sort.Strings(names)
		if k.multi {
			parts = append(parts, "login expired: "+strings.Join(names, ","))
		} else {
			parts = append(parts, "login expired")
		}
	}
	return strings.Join(parts, " · ")
}

// report puts the status on the Rex tab (tabStatus sends it only when it
// changed): an expired login blocks (kauth), failing pods are an error,
// otherwise idle once the pods tab has loaded.
func (k *kview) report() {
	if k.tab == nil {
		return
	}
	k.stMu.Lock()
	msg, expired, podsIn := k.statusText(), len(k.expired) > 0, k.podsIn
	k.stMu.Unlock()
	switch {
	case expired:
		k.tab.Blocked("auth", "kview: "+msg)
	case msg != "":
		k.tab.Set("error", "kview: "+msg)
	case podsIn:
		k.tab.Set("idle", "kview: no failing pods")
	}
}

// ── sorting ────────────────────────────────────────────────────────────

const (
	sortName = iota
	sortAge
	sortStatus
	sortRestarts
	sortCPU
	sortMem
)

// sorts are the tab's sort keys, k9s-like: shift+N name, A age, S status,
// T restarts, C CPU, M memory.
func (k *kview) sorts(which ...int) []tui.SortKey {
	all := map[int]tui.SortKey{
		sortName:     {Key: "N", Name: "name", Cmp: k.cmpBy(func(v kvSortable) string { return v.name })},
		sortAge:      {Key: "A", Name: "age", Desc: false, Cmp: k.cmpNum(func(v kvSortable) int64 { return -v.created.Unix() })},
		sortStatus:   {Key: "S", Name: "status", Cmp: k.cmpBy(func(v kvSortable) string { return v.status })},
		sortRestarts: {Key: "T", Name: "restarts", Desc: true, Cmp: k.cmpNum(func(v kvSortable) int64 { return v.restarts })},
		sortCPU:      {Key: "C", Name: "cpu", Desc: true, Cmp: k.cmpNum(func(v kvSortable) int64 { return v.cpu })},
		sortMem:      {Key: "M", Name: "memory", Desc: true, Cmp: k.cmpNum(func(v kvSortable) int64 { return v.mem })},
	}
	out := make([]tui.SortKey, len(which))
	for i, w := range which {
		out[i] = all[w]
	}
	return out
}

// kvSortable is what the sort keys compare of a row's object.
type kvSortable struct {
	name     string // namespace/name, then cluster
	created  time.Time
	status   string // the worst first: "0…" failing, "1…" waiting, "2…" fine
	restarts int64
	cpu, mem int64 // -1: no metrics
}

func (k *kview) sortable(it tui.Item) kvSortable {
	o, ok := k.obj(it.Key)
	if !ok {
		return kvSortable{name: it.Key, cpu: -1, mem: -1}
	}
	rank := func(badge string) string {
		switch badge {
		case "FAIL":
			return "0"
		case "WAIT":
			return "1"
		}
		return "2"
	}
	v := kvSortable{cpu: -1, mem: -1}
	switch o := o.(type) {
	case kube.On[kube.Pod]:
		p := o.V
		v = kvSortable{name: p.Namespace + "/" + p.Name + "|" + o.C.Short, created: p.Created, status: rank(p.Badge()) + p.Status(),
			restarts: int64(p.Restarts), cpu: -1, mem: -1}
		if u, ok := k.metrics.pod(o.C, p.Namespace, p.Name); ok {
			v.cpu, v.mem = u.cpuM, u.memB
		}
	case kube.On[kube.Workload]:
		w := o.V
		st := "2"
		if w.Ready < w.Desired {
			st = "1"
			if w.Ready == 0 {
				st = "0"
			}
		}
		v.name, v.created, v.status = w.Namespace+"/"+w.Name+"|"+o.C.Short, w.Created, st+w.Kind
	case kube.On[kube.Service]:
		v.name = o.V.Namespace + "/" + o.V.Name + "|" + o.C.Short
	case kvJob:
		if o.job != nil {
			v.name = o.job.Namespace + "/" + o.job.Name + "|" + o.c.Short
		} else {
			v.name = o.cron.Namespace + "/" + o.cron.Name + "|" + o.c.Short
		}
	case kube.On[kube.Node]:
		n := o.V
		st := "2"
		if !n.Ready {
			st = "0"
		} else if n.Unschedulable || len(n.Pressure) > 0 {
			st = "1"
		}
		v.name, v.created, v.status = n.Name+"|"+o.C.Short, n.Created, st
		if u, ok := k.metrics.node(o.C, n.Name); ok {
			v.cpu, v.mem = u.cpuM, u.memB
		}
	case kvRes:
		v.name, v.created = o.ns+"/"+o.name+"|"+o.c.Short, o.created
	}
	return v
}

func (k *kview) cmpBy(f func(kvSortable) string) func(a, b tui.Item) int {
	return func(a, b tui.Item) int {
		x, y := k.sortable(a), k.sortable(b)
		return cmp.Compare(f(x), f(y)) // ties keep the load's order: by name
	}
}

func (k *kview) cmpNum(f func(kvSortable) int64) func(a, b tui.Item) int {
	return func(a, b tui.Item) int {
		x, y := k.sortable(a), k.sortable(b)
		return cmp.Compare(f(x), f(y)) // ties keep the load's order: by name
	}
}

// ── pods ───────────────────────────────────────────────────────────────

const podHeader = "NAMESPACE/NAME"

var podTab = kviewTab[kube.Pod]{
	view: "pods", prefix: "pod|", metrics: "pods",
	queries: [][]string{{"get", "pods", "-A", "-o", "json"}},
	parse:   parseOne(kube.ParsePods),
	rows: func(k *kview, rows []kube.On[kube.Pod]) (string, []tui.Item, map[string]any) {
		fleet.Sort(rows, func(p kube.Pod) (string, string) { return p.Namespace, p.Name })
		w := len(podHeader)
		for _, r := range rows {
			w = max(w, len(r.V.Namespace)+1+len(r.V.Name))
		}
		header := fmt.Sprintf("%-*s  %-4s %-18s %3s  %-6s %-7s %s", w, podHeader, "READY", "STATUS", "RST", "CPU", "MEM", "AGE")
		objs := map[string]any{}
		items := make([]tui.Item, 0, len(rows))
		for _, r := range rows {
			ref := r.V.Namespace + "/" + r.V.Name
			key := "pod|" + r.C.Name + "|" + ref
			objs[key] = r
			cpu, mem := "-", "-"
			if u, ok := k.metrics.pod(r.C, r.V.Namespace, r.V.Name); ok {
				cpu, mem = u.cpu, u.mem
			}
			ready := fmt.Sprintf("%d/%d", r.V.Ready, r.V.Total)
			cells := fmt.Sprintf("%-*s  %-5s %-18s %3d  %-6s %-7s %s", w, ref, ready,
				r.V.Status(), r.V.Restarts, cpu, mem, kube.Age(r.V.Created))
			items = append(items, tui.Item{Key: key, Label: k.row(r.C, cells), Badge: r.V.Badge(), Name: kvName(r.C, "pod", ref),
				Sig: fmt.Sprintf("%s|%s|%d", ready, r.V.Status(), r.V.Restarts)}) // not the age, CPU or memory
		}
		return header, items, objs
	},
}

// ── workloads ──────────────────────────────────────────────────────────

const workloadHeader = "KIND    NAMESPACE/NAME"

var workloadTab = kviewTab[kube.Workload]{
	view: "workloads", prefix: "wl|",
	queries: [][]string{{"get", "deployments,statefulsets,daemonsets", "-A", "-o", "json"}},
	parse:   parseOne(kube.ParseWorkloads),
	rows: func(k *kview, rows []kube.On[kube.Workload]) (string, []tui.Item, map[string]any) {
		fleet.Sort(rows, func(w kube.Workload) (string, string) { return w.Namespace, w.Name })
		w := len("NAMESPACE/NAME")
		for _, r := range rows {
			w = max(w, len(r.V.Namespace)+1+len(r.V.Name))
		}
		header := fmt.Sprintf("%-7s %-*s  %-7s %-5s %s", "KIND", w, "NAMESPACE/NAME", "READY", "AGE", "IMAGE")
		objs := map[string]any{}
		items := make([]tui.Item, 0, len(rows))
		for _, r := range rows {
			ref := r.V.Namespace + "/" + r.V.Name
			key := "wl|" + r.C.Name + "|" + r.V.Kind + "/" + ref
			objs[key] = r
			badge := ""
			if r.V.Ready < r.V.Desired {
				badge = "WAIT"
				if r.V.Ready == 0 {
					badge = "FAIL"
				}
			}
			image := ""
			if len(r.V.Containers) > 0 {
				image = r.V.Containers[0].Image
				if i := strings.LastIndex(image, "/"); i >= 0 {
					image = image[i+1:]
				}
			}
			kind := kube.ShortKind(r.V.Kind)
			ready := fmt.Sprintf("%d/%d", r.V.Ready, r.V.Desired)
			cells := fmt.Sprintf("%-7s %-*s  %-7s %-5s %s", kind, w, ref, ready, kube.Age(r.V.Created), image)
			items = append(items, tui.Item{Key: key, Label: k.row(r.C, cells), Badge: badge, Name: kvName(r.C, kind, ref),
				Sig: ready + "|" + image})
		}
		return header, items, objs
	},
}

// ── services ───────────────────────────────────────────────────────────

const serviceHeader = "NAMESPACE/NAME"

var serviceTab = kviewTab[kube.Service]{
	view: "services", prefix: "svc|",
	queries: [][]string{{"get", "services", "-A", "-o", "json"}},
	parse:   parseOne(kube.ParseServices),
	rows: func(k *kview, rows []kube.On[kube.Service]) (string, []tui.Item, map[string]any) {
		fleet.Sort(rows, serviceRef)
		w := len(serviceHeader)
		for _, r := range rows {
			w = max(w, len(r.V.Namespace)+1+len(r.V.Name))
		}
		header := fmt.Sprintf("%-*s  %-12s %s", w, serviceHeader, "TYPE", "PORTS")
		objs := map[string]any{}
		items := make([]tui.Item, 0, len(rows))
		for _, r := range rows {
			ref := r.V.Namespace + "/" + r.V.Name
			key := "svc|" + r.C.Name + "|" + ref
			objs[key] = r
			var kinds []string
			for _, p := range r.V.Ports {
				kinds = append(kinds, fmt.Sprintf("%d/%s", p.Port, detectKind(p)))
			}
			cells := fmt.Sprintf("%-*s  %-12s %s", w, ref, r.V.Type, strings.Join(kinds, " "))
			items = append(items, tui.Item{Key: key, Label: k.row(r.C, cells), Name: kvName(r.C, "svc", ref),
				Sig: r.V.Type + "|" + strings.Join(kinds, " ")})
		}
		return header, items, objs
	},
}

// ── nodes ──────────────────────────────────────────────────────────────

const nodeHeader = "NAME"

var nodeTab = kviewTab[kube.Node]{
	view: "nodes", prefix: "node|", metrics: "nodes",
	queries: [][]string{{"get", "nodes", "-o", "json"}},
	parse:   parseOne(kube.ParseNodes),
	rows: func(k *kview, rows []kube.On[kube.Node]) (string, []tui.Item, map[string]any) {
		fleet.Sort(rows, func(n kube.Node) (string, string) { return "", n.Name })
		w, rw, vw := len(nodeHeader), 5, 7
		for _, r := range rows {
			w, rw, vw = max(w, len(r.V.Name)), max(rw, len(strings.Join(r.V.Roles, ","))), max(vw, len(r.V.Version))
		}
		header := fmt.Sprintf("%-*s  %-*s %-*s %-5s %-11s %-12s %s", w, nodeHeader, rw, "ROLES", vw, "VERSION", "AGE", "CPU", "MEM", "STATUS")
		objs := map[string]any{}
		items := make([]tui.Item, 0, len(rows))
		for _, r := range rows {
			key := "node|" + r.C.Name + "|" + r.V.Name
			objs[key] = r
			state, badge := "Ready", ""
			if !r.V.Ready {
				state, badge = "NotReady", "FAIL"
			}
			if r.V.Unschedulable {
				state += ",SchedulingDisabled"
				if badge == "" {
					badge = "WAIT"
				}
			}
			if len(r.V.Pressure) > 0 {
				state += " " + strings.Join(r.V.Pressure, ",")
				if badge == "" {
					badge = "WAIT"
				}
			}
			cpu, mem := "-", "-"
			if u, ok := k.metrics.node(r.C, r.V.Name); ok {
				cpu, mem = u.cpu+" "+u.cpuPct, u.mem+" "+u.memPct
			}
			cells := fmt.Sprintf("%-*s  %-*s %-*s %-5s %-11s %-12s %s", w, r.V.Name, rw, orDash(strings.Join(r.V.Roles, ",")), vw, r.V.Version,
				kube.Age(r.V.Created), cpu, mem, state)
			items = append(items, tui.Item{Key: key, Label: k.row(r.C, cells), Badge: badge, Name: kvName(r.C, "node", r.V.Name),
				Sig: state + "|" + r.V.Version}) // not the age, CPU or memory
		}
		return header, items, objs
	},
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ── warnings ───────────────────────────────────────────────────────────

const warningHeader = "WHEN      NAMESPACE  OBJECT  REASON  MESSAGE"

type kvWarning struct {
	c          kube.Context
	ns         string
	kind, name string
}

var warningTab = kviewTab[kube.Event]{
	view: "warnings", prefix: "warn|",
	queries: [][]string{{"get", "events", "-A", "--field-selector", "type=Warning", "-o", "json"}},
	parse:   parseOne(kube.ParseEvents),
	rows: func(k *kview, evs []kube.On[kube.Event]) (string, []tui.Item, map[string]any) {
		type row struct {
			c    kube.Context
			e    kube.Event
			when time.Time
		}
		rows := make([]row, len(evs))
		for i, e := range evs {
			rows[i] = row{e.C, e.V, e.V.When()}
		}
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].when.After(rows[j].when) })
		objs := map[string]any{}
		items := make([]tui.Item, 0, len(rows))
		for _, r := range rows {
			obj := kube.ShortKind(r.e.InvolvedObject.Kind) + "/" + r.e.InvolvedObject.Name
			key := "warn|" + r.c.Name + "|" + r.e.Metadata.UID
			objs[key] = kvWarning{r.c, r.e.Metadata.Namespace, r.e.InvolvedObject.Kind, r.e.InvolvedObject.Name}
			count := ""
			if n := r.e.Count; r.e.Series != nil {
				count = fmt.Sprintf(" x%d", r.e.Series.Count)
			} else if n > 1 {
				count = fmt.Sprintf(" x%d", n)
			}
			cells := fmt.Sprintf("%-8s  %s %s %s%s  %s", kube.Age(r.when)+" ago", r.e.Metadata.Namespace, obj, r.e.Reason, count,
				truncate(oneLine(tui.Clean(r.e.Message)), 200))
			items = append(items, tui.Item{Key: key, Label: k.row(r.c, cells), Name: kvName(r.c, "event of", r.e.Metadata.Namespace+"/"+obj),
				Sig: count + "|" + r.e.Message}) // seen again: x3 → x4
		}
		return warningHeader, items, objs
	},
}
