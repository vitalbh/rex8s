package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kview: a k9s-like browser of this pane's cluster (every namespace), or of
// every cluster with -A. Tabs: pods, workloads, services, jobs, nodes,
// warnings — each refreshes by itself. Letters act on the row under the
// cursor (kview_actions.go): logs and shells open in Rex splits, describe
// and YAML in a viewer, changes (delete, restart, run a CronJob) go through
// the production guard.
//
//	kview [-A | --context CTX] [pods|workloads|services|jobs|nodes|warnings]
type kview struct {
	a        *app
	cl       kube.Client
	fleet    *fleet.Reader // remembers unreachable clusters between refreshes
	clusters []kube.Context
	multi    bool

	mu      sync.Mutex
	objs    map[string]any    // row Key → what it shows (pod, workload, …), from the last load
	headers map[string]string // view → its column titles, sized by the last load
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
)

var kviewNames = []string{"pods", "workloads", "services", "jobs", "nodes", "warnings"}

// kviewOpts is what kview opens on.
type kviewOpts struct {
	all     bool   // -A: every cluster
	ctxName string // --context ("" with all, or for the pane's cluster)
	start   int    // the first tab
	filter  string // its filter
	ns      string // the namespace the pane is on, for the pane label ("" none)
}

func parseKviewArgs(args []string) (kviewOpts, error) {
	var o kviewOpts
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
		default:
			found := false
			for i, n := range kviewNames {
				if strings.HasPrefix(n, strings.ToLower(arg)) || (arg == "svc" && n == "services") || (arg == "deploy" && n == "workloads") {
					o.start, found = i, true
					break
				}
			}
			if !found {
				return o, fmt.Errorf("kview [-A | --context CTX] [%s]", strings.Join(kviewNames, "|"))
			}
		}
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

// kviewLabel is the pane label: "kview PROD use/payments", "kview euw",
// "kview PROD all" (-A with a production cluster in it).
func kviewLabel(clusters []kube.Context, all bool, ns string) string {
	if all {
		for _, c := range clusters {
			if c.Prod {
				return "kview PROD all"
			}
		}
		return "kview all"
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
	k := &kview{a: a, cl: r.Client, fleet: r, objs: map[string]any{}, headers: map[string]string{}}
	title := ""
	if o.all {
		if k.clusters, err = a.pickClusters(nil); err != nil {
			return err
		}
		if len(k.clusters) == 0 {
			return errors.New("no clusters")
		}
		k.multi, title = true, fmt.Sprintf("kview · %d clusters", len(k.clusters))
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
	rex.Rename(kviewLabel(k.clusters, o.all, o.ns))
	return tui.RunBrowser(tui.BrowserOptions{Title: title, Views: k.views(), Start: o.start, Filter: o.filter,
		Action: k.action, Reload: k.fleet.Retry})
}

func (k *kview) views() []tui.View {
	every := func(one, many time.Duration) time.Duration {
		if k.multi {
			return many
		}
		return one
	}
	return []tui.View{
		{Name: "pods", Header: k.header("pods", podHeader), Load: podTab.loader(k), Every: every(5*time.Second, 15*time.Second),
			Actions: []tui.Action{act("l", "logs"), act("s", "shell"), act("d", "describe"), act("e", "events"), act("y", "yaml"),
				act("w", "why"), act("a", "ask"), danger("ctrl+d", "delete")}},
		{Name: "workloads", Header: k.header("workloads", workloadHeader), Load: workloadTab.loader(k), Every: every(10*time.Second, 20*time.Second),
			Actions: []tui.Action{act("enter", "pods"), act("l", "logs"), danger("r", "restart"), act("d", "describe"), act("y", "yaml")}},
		{Name: "services", Header: k.header("services", serviceHeader), Load: serviceTab.loader(k), Every: 30 * time.Second,
			Actions: []tui.Action{act("enter", "forward"), act("d", "describe"), act("y", "yaml")}},
		{Name: "jobs", Header: k.header("jobs", "KIND NAMESPACE/NAME"), Load: jobTab.loader(k), Every: every(15*time.Second, 30*time.Second),
			Actions: []tui.Action{act("l", "logs"), danger("r", "run now"), act("d", "describe"), act("y", "yaml")}},
		{Name: "nodes", Header: k.header("nodes", nodeHeader), Load: nodeTab.loader(k), Every: 30 * time.Second,
			Actions: []tui.Action{act("s", "shell"), act("d", "describe"), act("y", "yaml")}},
		{Name: "warnings", Header: k.header("warnings", warningHeader), Load: warningTab.loader(k), Every: every(10*time.Second, 20*time.Second),
			Actions: []tui.Action{act("enter", "describe")}},
	}
}

func act(key, label string) tui.Action { return tui.Action{Key: key, Label: label} }

// danger: an action that changes the cluster — y/N in the browser first.
func danger(key, label string) tui.Action { return tui.Action{Key: key, Label: label, Confirm: true} }

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
		res := fleet.ReadEach(k.fleet, k.clusters, t.queries, t.parse, func(c kube.Context, rows []kube.On[T], skip *fleet.Skip) {
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
		if res.OK == 0 && len(res.Skipped) > 0 {
			return nil, errors.New(strings.Join(res.Strings(), " · "))
		}
		mu.Lock()
		defer mu.Unlock()
		prev = fresh
		return build(true), nil
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

// ── pods ───────────────────────────────────────────────────────────────

const podHeader = "NAMESPACE/NAME"

var podTab = kviewTab[kube.Pod]{
	view: "pods", prefix: "pod|",
	queries: [][]string{{"get", "pods", "-A", "-o", "json"}},
	parse:   parseOne(kube.ParsePods),
	rows: func(k *kview, rows []kube.On[kube.Pod]) (string, []tui.Item, map[string]any) {
		fleet.Sort(rows, func(p kube.Pod) (string, string) { return p.Namespace, p.Name })
		w := len(podHeader)
		for _, r := range rows {
			w = max(w, len(r.V.Namespace)+1+len(r.V.Name))
		}
		header := fmt.Sprintf("%-*s  %-4s %-18s %3s  %s", w, podHeader, "READY", "STATUS", "RST", "AGE")
		objs := map[string]any{}
		items := make([]tui.Item, 0, len(rows))
		for _, r := range rows {
			ref := r.V.Namespace + "/" + r.V.Name
			key := "pod|" + r.C.Name + "|" + ref
			objs[key] = r
			cells := fmt.Sprintf("%-*s  %-5s %-18s %3d  %s", w, ref, fmt.Sprintf("%d/%d", r.V.Ready, r.V.Total),
				r.V.Status(), r.V.Restarts, kube.Age(r.V.Created))
			items = append(items, tui.Item{Key: key, Label: k.row(r.C, cells), Badge: r.V.Badge(), Name: kvName(r.C, "pod", ref)})
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
			cells := fmt.Sprintf("%-7s %-*s  %-7s %-5s %s", kind, w, ref,
				fmt.Sprintf("%d/%d", r.V.Ready, r.V.Desired), kube.Age(r.V.Created), image)
			items = append(items, tui.Item{Key: key, Label: k.row(r.C, cells), Badge: badge, Name: kvName(r.C, kind, ref)})
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
			items = append(items, tui.Item{Key: key, Label: k.row(r.C, cells), Name: kvName(r.C, "svc", ref)})
		}
		return header, items, objs
	},
}

// ── jobs ───────────────────────────────────────────────────────────────

type kvJob struct {
	c    kube.Context
	job  *kube.Job
	cron *kube.CronJob
}

var jobTab = kviewTab[jobsAndCrons]{
	view: "jobs", prefix: "job|",
	queries: jobQueries,
	parse:   parseJobsAndCrons,
	rows: func(k *kview, read []kube.On[jobsAndCrons]) (string, []tui.Item, map[string]any) {
		w := 0
		for _, r := range read {
			w = max(w, jobNameWidth(r.V.jobs, r.V.crons))
		}
		objs, names := map[string]any{}, map[string]string{}
		var rows []jobRow
		for _, r := range read {
			jobsByKey, cronsByKey, cronStates := map[string]kube.Job{}, map[string]kube.CronJob{}, map[string]string{}
			rows = append(rows, jobRows(r.V.jobs, r.V.crons, "", "job|"+r.C.Name+"|", k.row(r.C, ""), w, jobsByKey, cronsByKey, cronStates)...)
			for key, j := range jobsByKey {
				objs[key] = kvJob{c: r.C, job: &j}
				names[key] = kvName(r.C, "job", j.Namespace+"/"+j.Name)
			}
			for key, cj := range cronsByKey {
				objs[key] = kvJob{c: r.C, cron: &cj}
				names[key] = kvName(r.C, "cronjob", cj.Namespace+"/"+cj.Name)
			}
		}
		sortJobRows(rows)
		items := make([]tui.Item, len(rows))
		for i, r := range rows {
			items[i] = r.item
			items[i].Name = names[r.item.Key]
		}
		return "", items, objs // the header stays "KIND NAMESPACE/NAME"
	},
}

// ── nodes ──────────────────────────────────────────────────────────────

const nodeHeader = "NAME"

var nodeTab = kviewTab[kube.Node]{
	view: "nodes", prefix: "node|",
	queries: [][]string{{"get", "nodes", "-o", "json"}},
	parse:   parseOne(kube.ParseNodes),
	rows: func(k *kview, rows []kube.On[kube.Node]) (string, []tui.Item, map[string]any) {
		fleet.Sort(rows, func(n kube.Node) (string, string) { return "", n.Name })
		w, rw, vw := len(nodeHeader), 5, 7
		for _, r := range rows {
			w, rw, vw = max(w, len(r.V.Name)), max(rw, len(strings.Join(r.V.Roles, ","))), max(vw, len(r.V.Version))
		}
		header := fmt.Sprintf("%-*s  %-*s %-*s %-5s %s", w, nodeHeader, rw, "ROLES", vw, "VERSION", "AGE", "STATUS")
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
			cells := fmt.Sprintf("%-*s  %-*s %-*s %-5s %s", w, r.V.Name, rw, orDash(strings.Join(r.V.Roles, ",")), vw, r.V.Version, kube.Age(r.V.Created), state)
			items = append(items, tui.Item{Key: key, Label: k.row(r.C, cells), Badge: badge, Name: kvName(r.C, "node", r.V.Name)})
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
			items = append(items, tui.Item{Key: key, Label: k.row(r.c, cells), Name: kvName(r.c, "event of", r.e.Metadata.Namespace+"/"+obj)})
		}
		return warningHeader, items, objs
	},
}
