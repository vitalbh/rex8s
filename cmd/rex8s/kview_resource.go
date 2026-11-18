package main

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kview's ":KIND" tab, like k9s: any kind the API server has (ingresses,
// configmaps, secrets, PVCs, HPAs, CRDs and their objects), with the
// columns the server prints for it (kubectl get -o wide: a CRD's printer
// columns too). describe, YAML, delete (through the guard) and the filter
// work as on the other tabs; esc goes back to them.

// kviewFixed: what ":" opens as one of the fixed tabs instead.
var kviewFixed = map[string]int{
	"pods": vPods, "pod": vPods, "po": vPods,
	"workloads": vWorkloads, "deploy": vWorkloads, "deployment": vWorkloads, "deployments": vWorkloads,
	"sts": vWorkloads, "statefulset": vWorkloads, "statefulsets": vWorkloads, "ds": vWorkloads, "daemonset": vWorkloads, "daemonsets": vWorkloads,
	"svc": vServices, "service": vServices, "services": vServices,
	"jobs": vJobs, "job": vJobs, "cronjobs": vJobs, "cronjob": vJobs, "cj": vJobs,
	"nodes": vNodes, "node": vNodes, "no": vNodes,
	"warnings": vWarnings, "events": vWarnings, "ev": vWarnings,
}

// kvAPI caches kubectl api-resources (of the first cluster that answers).
type kvAPI struct {
	mu    sync.Mutex
	list  []kube.APIResource
	state int // apiUnread, apiReading, apiRead
	done  chan struct{}
}

const (
	apiUnread = iota
	apiReading
	apiRead
)

// apiResources is the cached list; the first call starts reading it. With
// wait, it waits for that read.
func (k *kview) apiResources(wait bool) []kube.APIResource {
	k.api.mu.Lock()
	if k.api.state == apiUnread {
		k.api.state, k.api.done = apiReading, make(chan struct{})
		go k.readAPIResources()
	}
	done, list := k.api.done, k.api.list
	k.api.mu.Unlock()
	if !wait {
		return list
	}
	<-done
	k.api.mu.Lock()
	defer k.api.mu.Unlock()
	return k.api.list
}

func (k *kview) readAPIResources() {
	var list []kube.APIResource
	for _, c := range k.clusters {
		out, err := k.cl.Get(c.Name, "api-resources")
		if err == nil {
			list = kube.ParseAPIResources(string(out))
			break
		}
	}
	k.api.mu.Lock()
	defer k.api.mu.Unlock()
	k.api.list, k.api.state = list, apiRead
	if list == nil {
		k.api.state = apiUnread // try again on the next ":"
	}
	close(k.api.done)
}

// resolve is the resource name means. Without a list of resources (none
// answered) the name goes to kubectl as typed; with -A too when it is not
// in the list (another cluster may have that CRD).
func (k *kview) resolve(name string) (kube.APIResource, bool) {
	list := k.apiResources(true)
	if r, ok := kube.FindAPIResource(list, name); ok {
		return r, true
	}
	if len(list) == 0 || k.multi {
		return kube.APIResource{Name: name, Kind: name, Namespaced: true}, true
	}
	return kube.APIResource{}, false
}

// command runs ":TEXT".
func (k *kview) command(text string) tui.Result {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(text), ":"))
	if name == "" {
		return tui.Result{}
	}
	if strings.ContainsAny(name, ", /") {
		return tui.Result{Err: errors.New("one kind at a time: :ingress, :cm, :crd…")}
	}
	if v, ok := kviewFixed[name]; ok {
		return tui.Result{Goto: v + 1}
	}
	r, ok := k.resolve(name)
	if !ok {
		return tui.Result{Err: fmt.Errorf("the server has no resource type %q (kubectl api-resources)", name)}
	}
	return k.openResource(r, nil, "")
}

// complete suggests kinds for ":", by their full name, so nobody has to
// remember short names: each suggestion is "NAME  (short, Kind)" and the
// browser types the first word. A query matches the name, a short name or
// the Kind anywhere ("map" finds configmaps); exact matches come first, then
// names starting with it, then the rest, alphabetically.
func (k *kview) complete(prefix string) []string {
	q := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(prefix, ":")))
	type cand struct {
		name, label string
		rank        int
	}
	var cs []cand
	seen := map[string]bool{}
	add := func(name string, also []string, label string) {
		if seen[name] {
			return
		}
		rank := -1
		switch {
		case q == "":
			rank = 0
		case name == q || slices.Contains(also, q):
			rank = 0
		case strings.HasPrefix(name, q):
			rank = 1
		case slices.ContainsFunc(also, func(a string) bool { return strings.HasPrefix(a, q) }):
			rank = 2
		case strings.Contains(name, q) || slices.ContainsFunc(also, func(a string) bool { return strings.Contains(a, q) }):
			rank = 3
		}
		if rank < 0 {
			return
		}
		seen[name] = true
		cs = append(cs, cand{name, label, rank})
	}
	// the tabs that aren't a single kind
	add("workloads", []string{"deploy", "sts", "ds"}, "workloads  (deployments, statefulsets, daemonsets)")
	add("warnings", []string{"events", "ev"}, "warnings  (Warning events)")
	for _, r := range k.apiResources(false) {
		also := append(slices.Clone(r.Short), strings.ToLower(r.Kind))
		label := r.Name
		var extra []string
		if len(r.Short) > 0 {
			extra = append(extra, strings.Join(r.Short, ", "))
		}
		if r.Group != "" {
			extra = append(extra, r.Group)
		}
		if len(extra) > 0 {
			label += "  (" + strings.Join(extra, " · ") + ")"
		}
		add(r.Name, also, label)
	}
	slices.SortFunc(cs, func(a, b cand) int { return cmp.Or(cmp.Compare(a.rank, b.rank), cmp.Compare(a.name, b.name)) })
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.label
	}
	return out
}

// openResource opens the tab of r, showing only the rows only accepts (nil:
// all).
func (k *kview) openResource(r kube.APIResource, only func(tui.Item) bool, onlyLabel string) tui.Result {
	k.keep(nil, "res|") // the kind shown before
	v := k.resourceView(r)
	return tui.Result{Open: &v, Only: only, OnlyLabel: onlyLabel}
}

// kvRes is a row of the ":KIND" tab.
type kvRes struct {
	c        kube.Context
	res      kube.APIResource
	ns, name string
	created  time.Time // from AGE: about when
}

// ref is "ns/name", or "name" for a cluster-scoped object.
func (r kvRes) ref() string {
	if r.ns == "" {
		return r.name
	}
	return r.ns + "/" + r.name
}

// kvTableRow is a row of kubectl's table, with the table's columns.
type kvTableRow struct {
	cols  []string
	cells []string
}

func (t kvTableRow) cell(col string) string {
	if i := slices.Index(t.cols, col); i >= 0 {
		return t.cells[i]
	}
	return ""
}

func parseResTable(_ kube.Context, out [][]byte) ([]kvTableRow, error) {
	t := kube.ParseTable(string(out[0]))
	if len(t.Rows) > 0 && t.Col("NAME") < 0 {
		return nil, errors.New("no NAME column in kubectl's table")
	}
	rows := make([]kvTableRow, len(t.Rows))
	for i, r := range t.Rows {
		rows[i] = kvTableRow{t.Columns, r}
	}
	return rows, nil
}

func (k *kview) resourceView(r kube.APIResource) tui.View {
	full := r.Full()
	q := []string{"get", full, "-o", "wide"}
	if r.Namespaced {
		q = append(q, "-A")
	}
	kind := strings.ToLower(r.Kind)
	tab := kviewTab[kvTableRow]{
		view: "res:" + full, prefix: "res|",
		queries: [][]string{q},
		parse:   parseResTable,
		rows: func(k *kview, read []kube.On[kvTableRow]) (string, []tui.Item, map[string]any) {
			return resRows(k, r, kind, read)
		},
	}
	every := 15 * time.Second
	if k.multi {
		every = 30 * time.Second
	}
	return tui.View{Name: ":" + r.Name, Header: k.header(tab.view, "NAMESPACE/NAME"), Load: tab.loader(k), Every: every,
		Actions: []tui.Action{act("d", "describe"), act("y", "yaml"), many("ctrl+d", "delete")},
		Sorts:   k.sorts(sortName, sortAge)}
}

// resCellMax caps a column's width (labels, long messages).
const resCellMax = 48

func resRows(k *kview, r kube.APIResource, kind string, read []kube.On[kvTableRow]) (string, []tui.Item, map[string]any) {
	ref := func(t kvTableRow) string {
		if ns := t.cell("NAMESPACE"); ns != "" {
			return ns + "/" + t.cell("NAME")
		}
		return t.cell("NAME")
	}
	fleet.Sort(read, func(t kvTableRow) (string, string) { return t.cell("NAMESPACE"), t.cell("NAME") })
	var cols []string
	for _, row := range read {
		for _, c := range row.V.cols {
			if c != "NAMESPACE" && c != "NAME" && !slices.Contains(cols, c) {
				cols = append(cols, c)
			}
		}
	}
	first := "NAME"
	if r.Namespaced {
		first = "NAMESPACE/NAME"
	}
	nameW := len(first)
	widths := make([]int, len(cols))
	for i, c := range cols {
		widths[i] = utf8.RuneCountInString(c)
	}
	for _, row := range read {
		nameW = max(nameW, utf8.RuneCountInString(ref(row.V)))
		for i, c := range cols {
			widths[i] = max(widths[i], min(resCellMax, utf8.RuneCountInString(row.V.cell(c))))
		}
	}
	line := func(name string, cells []string) string {
		var s strings.Builder
		s.WriteString(padRunes(name, nameW))
		for i, c := range cells {
			s.WriteString("  ")
			if i == len(cells)-1 {
				s.WriteString(truncate(c, resCellMax*2))
			} else {
				s.WriteString(padRunes(truncate(c, resCellMax), widths[i]))
			}
		}
		return s.String()
	}
	header := line(first, cols)
	objs := map[string]any{}
	items := make([]tui.Item, 0, len(read))
	for _, row := range read {
		t := row.V
		o := kvRes{c: row.C, res: r, ns: t.cell("NAMESPACE"), name: t.cell("NAME")}
		if d, ok := parseKubeAge(t.cell("AGE")); ok {
			o.created = time.Now().Add(-d)
		}
		key := "res|" + r.Full() + "|" + row.C.Name + "|" + o.ref()
		objs[key] = o
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = t.cell(c)
		}
		items = append(items, tui.Item{Key: key, Label: k.row(row.C, line(ref(t), cells)), Name: kvName(row.C, kind, o.ref())})
	}
	return header, items, objs
}

// padRunes pads s with spaces to w runes.
func padRunes(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// parseKubeAge reads kubectl's AGE: "45s", "12m", "5h3m", "4d", "2y110d".
func parseKubeAge(s string) (time.Duration, bool) {
	units := map[byte]time.Duration{'s': time.Second, 'm': time.Minute, 'h': time.Hour, 'd': 24 * time.Hour, 'y': 365 * 24 * time.Hour}
	var total time.Duration
	num := ""
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; {
		case ch >= '0' && ch <= '9':
			num += string(ch)
		case units[ch] != 0 && num != "":
			n, _ := strconv.Atoi(num)
			total += time.Duration(n) * units[ch]
			num = ""
		default:
			return 0, false
		}
	}
	return total, num == "" && s != ""
}

func (k *kview) resAction(v kvRes, key string) tui.Result {
	full := v.res.Full()
	var ns []string
	if v.ns != "" {
		ns = []string{"-n", v.ns}
	}
	switch key {
	case "d":
		return k.text(v.c, "describe "+v.res.Name+" "+v.ref(), append(append([]string{"describe", full}, ns...), v.name)...)
	case "y":
		return k.text(v.c, v.res.Name+" "+v.ref(), append(append([]string{"get", full}, ns...), v.name, "-o", "yaml")...)
	case "ctrl+d":
		return k.guarded(v.c, append(ns, "delete", full, v.name)...)
	}
	return tui.Result{}
}
