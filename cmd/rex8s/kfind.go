package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
	"golang.org/x/term"
)

// findKind is a kind kfind searches: its kind as the API names it, the
// resource kubectl gets and describes it by, and its order in the results.
type findKind struct{ kind, resource string }

var findKinds = []findKind{
	{"Pod", "pods"}, {"Deployment", "deployments.apps"}, {"StatefulSet", "statefulsets.apps"},
	{"DaemonSet", "daemonsets.apps"}, {"Service", "services"}, {"Ingress", "ingresses.networking.k8s.io"},
	{"ConfigMap", "configmaps"}, {"Secret", "secrets"}, {"Job", "jobs.batch"}, {"CronJob", "cronjobs.batch"},
	{"PersistentVolumeClaim", "persistentvolumeclaims"},
	{"Kustomization", "kustomizations.kustomize.toolkit.fluxcd.io"},
	{"HelmRelease", "helmreleases.helm.toolkit.fluxcd.io"},
}

// findColumns prints what kfind needs of each object and nothing else: a
// Secret's data never reaches the output.
var findColumns = []string{"-o", "custom-columns=KIND:.kind,NAMESPACE:.metadata.namespace,NAME:.metadata.name,CREATED:.metadata.creationTimestamp", "--no-headers"}

// findQueries: one kubectl get for the built-in kinds; Secrets (often not
// listable cluster-wide) and the Flux CRDs (not on every cluster) are best
// effort, each its own get, so their failure never skips a cluster.
func findQueries() [][]string {
	var core, secrets, flux []string
	for _, k := range findKinds {
		switch {
		case k.kind == "Secret":
			secrets = append(secrets, k.resource)
		case strings.Contains(k.resource, "fluxcd.io"):
			flux = append(flux, k.resource)
		default:
			core = append(core, k.resource)
		}
	}
	get := func(rs []string) []string {
		return append([]string{"get", strings.Join(rs, ","), "-A"}, findColumns...)
	}
	return [][]string{get(core), fleet.Optional(get(secrets)...), fleet.Optional(get(flux)...)}
}

// found is one object kfind found.
type found struct {
	Kind, Namespace, Name string
	Created               time.Time
}

func (f found) resource() string {
	for _, k := range findKinds {
		if k.kind == f.Kind {
			return k.resource
		}
	}
	return strings.ToLower(f.Kind)
}

func kindRank(kind string) int {
	for i, k := range findKinds {
		if k.kind == kind {
			return i
		}
	}
	return len(findKinds)
}

// findMatch: does an object's name match what kfind looks for? A
// substring, case-insensitive (exact: the whole name). With a slash, the
// query is NS/NAME.
func findMatch(ns, name, query string, exact bool) bool {
	q := strings.ToLower(query)
	subject := strings.ToLower(name)
	if strings.Contains(q, "/") {
		subject = strings.ToLower(ns + "/" + name)
	}
	if exact {
		return subject == q
	}
	return strings.Contains(subject, q)
}

// parseFound reads the custom-columns lines of the find queries, keeping
// the objects that match.
func parseFound(outs [][]byte, query string, exact bool) []found {
	var res []found
	for _, out := range outs {
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) != 4 || !findMatch(f[1], f[2], query, exact) {
				continue
			}
			ns := f[1]
			if ns == "<none>" {
				ns = ""
			}
			created, _ := time.Parse(time.RFC3339, f[3])
			res = append(res, found{Kind: f[0], Namespace: ns, Name: f[2], Created: created})
		}
	}
	return res
}

// kfind [-x] NAME: an object by name on every cluster — pods, workloads,
// services, ingresses, config maps, secrets (names only), jobs, cronjobs,
// PVCs, Flux Kustomizations and HelmReleases. Each cluster says how many
// it has as it answers; then a picker of them all, and enter acts on one.
func (a *app) kfind(args []string) error {
	exact, query := false, ""
	for _, arg := range args {
		switch {
		case arg == "-x" || arg == "--exact":
			exact = true
		case query == "":
			query = arg
		default:
			return errors.New("usage: kfind [-x] NAME")
		}
	}
	if query == "" {
		return errors.New("usage: kfind [-x] NAME (a substring of the name; -x: the exact name; NS/NAME works too)")
	}
	r, err := a.reader()
	if err != nil {
		return err
	}
	raw, err := kube.Load(a.src)
	if err != nil {
		return err
	}
	clusters := fleet.All(raw, a.cfg)
	look, none := fmt.Sprintf("names containing %q", query), fmt.Sprintf("no name contains %q", query)
	if exact {
		look, none = fmt.Sprintf("objects named %q", query), fmt.Sprintf("no object named %q", query)
	}
	fmt.Fprintln(os.Stderr, tui.Dim.Render(fmt.Sprintf("looking for %s on %d clusters…", look, len(clusters))))
	w := 0
	for _, c := range clusters {
		w = max(w, len(c.Short))
	}
	var mu sync.Mutex
	res := fleet.ReadEach(r, clusters, findQueries(),
		func(_ kube.Context, outs [][]byte) ([]found, error) { return parseFound(outs, query, exact), nil },
		func(c kube.Context, rows []kube.On[found], skip *fleet.Skip) {
			mu.Lock()
			defer mu.Unlock()
			name := fmt.Sprintf("%-*s", w, c.Short)
			switch {
			case skip != nil:
				fmt.Fprintln(os.Stderr, tui.Dim.Render(name+"  skipped: "+skip.Reason))
			case len(rows) == 0:
				fmt.Fprintln(os.Stderr, tui.Dim.Render(name+"  none"))
			default:
				n := fmt.Sprintf("%d matches", len(rows))
				if len(rows) == 1 {
					n = "1 match"
				}
				fmt.Fprintln(os.Stderr, name+"  "+tui.Ok.Render(n))
			}
		})
	all := res.Rows
	if len(all) == 0 {
		where := plural(res.OK, "cluster")
		if len(res.Skipped) > 0 {
			where += fmt.Sprintf(" (%d skipped)", len(res.Skipped))
		}
		return fmt.Errorf("%s on %s", none, where)
	}
	sortFound(all)

	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		for _, f := range all { // piped: the list, for grep/awk
			fmt.Printf("%s\t%s\t%s\t%s\n", f.C.Short, f.V.Kind, nsName(f.V), kube.Age(f.V.Created))
		}
		return nil
	}
	items := findItems(all)
	it, err := tui.Run(tui.Options{Title: clustersTitle(fmt.Sprintf("kfind %q", query), res.OK, len(res.Skipped)), Items: items})
	if err != nil || it == nil {
		return err
	}
	i, _ := strconv.Atoi(it.Key)
	return a.findAction(cl, all[i].C, all[i].V)
}

func nsName(f found) string {
	if f.Namespace == "" {
		return f.Name
	}
	return f.Namespace + "/" + f.Name
}

// sortFound: by cluster, then kind (pods first), namespace and name.
func sortFound(all []kube.On[found]) {
	sort.SliceStable(all, func(i, j int) bool {
		x, y := all[i], all[j]
		if x.C.Short != y.C.Short {
			return x.C.Short < y.C.Short
		}
		if rx, ry := kindRank(x.V.Kind), kindRank(y.V.Kind); rx != ry {
			return rx < ry
		}
		if x.V.Namespace != y.V.Namespace {
			return x.V.Namespace < y.V.Namespace
		}
		return x.V.Name < y.V.Name
	})
}

// findItems: one picker row per object — cluster, kind, ns/name; its age.
func findItems(all []kube.On[found]) []tui.Item {
	wc, wk, wn := 0, 0, 0
	for _, f := range all {
		wc = max(wc, len(f.C.Short))
		wk = max(wk, len(f.V.Kind))
		wn = max(wn, len(nsName(f.V)))
	}
	items := make([]tui.Item, len(all))
	for i, f := range all {
		items[i] = tui.Item{Key: strconv.Itoa(i),
			Label: fmt.Sprintf("%-*s  %-*s  %-*s", wc, f.C.Short, wk, f.V.Kind, wn, nsName(f.V)),
			Hint:  kube.Age(f.V.Created), Badge: prodBadgeBool(f.C.Prod),
			Name: strings.ToLower(f.V.Kind) + " " + nsName(f.V) + " on " + f.C.Short}
	}
	return items
}

// findAction: what enter does with a found object. A pod gets kfail's
// actions (logs, why, shell…); a workload its logs; a service a
// port-forward; everything describe, and a cluster shell on its namespace.
func (a *app) findAction(cl kube.Client, c kube.Context, f found) error {
	if f.Kind == "Pod" {
		out, err := cl.Get(c.Name, "get", "pods", "-n", f.Namespace, "--field-selector", "metadata.name="+f.Name, "-o", "json")
		if err != nil {
			return err
		}
		ps, err := kube.ParsePods(out)
		if err != nil || len(ps) == 0 {
			return fmt.Errorf("pod %s/%s is gone from %s", f.Namespace, f.Name, c.Short)
		}
		return a.podAction(c, cl, ps[0], func() {})
	}
	var acts []tui.Item
	add := func(key, hint string) {
		acts = append(acts, tui.Item{Key: key, Label: fmt.Sprintf("%-9s", key), Hint: hint})
	}
	switch f.Kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		add("logs", "follow all its pods in a split (klogs)")
	case "Service":
		add("forward", "port-forward it in the background (kpf)")
	}
	add("describe", "kubectl describe, in a split")
	add("shell", "a shell on "+c.Short+"/"+nsOr(f.Namespace)+", in a split")
	act, err := tui.Run(tui.Options{Title: fmt.Sprintf("%s %s on %s", strings.ToLower(f.Kind), nsName(f), c.Short), Items: acts})
	if err != nil || act == nil {
		return err
	}
	self := filepath.Join(a.binDir, "rex8s")
	inRex := rex.Block() != ""
	switch act.Key {
	case "logs":
		out, err := cl.Get(c.Name, "get", f.resource(), "-n", f.Namespace, "--field-selector", "metadata.name="+f.Name, "-o", "json")
		if err != nil {
			return err
		}
		ws, err := kube.ParseWorkloads(out)
		if err != nil || len(ws) == 0 || ws[0].Selector == "" {
			return fmt.Errorf("%s %s on %s: no pod selector", strings.ToLower(f.Kind), nsName(f), c.Short)
		}
		spec := logSpec{c.Name, f.Namespace, ws[0].Selector, c.Short + " " + f.Name}.String()
		if !inRex {
			return a.wlogtail([]string{spec}, logOpts{})
		}
		return rex.SplitBehind("logs "+f.Name, self, "_wlogs", spec)
	case "forward":
		out, err := cl.Get(c.Name, "get", "services", "-n", f.Namespace, "--field-selector", "metadata.name="+f.Name, "-o", "json")
		if err != nil {
			return err
		}
		ss, err := kube.ParseServices(out)
		if err != nil || len(ss) == 0 {
			return fmt.Errorf("service %s on %s has no TCP port", nsName(f), c.Short)
		}
		return a.kpfStart(c, ss[0])
	case "describe":
		if !inRex {
			return a.describeTail(c.Name, f.Namespace, f.resource(), f.Name)
		}
		return rex.Split("describe "+f.Name, self, "_describe", c.Name, f.Namespace, f.resource(), f.Name)
	case "shell":
		if !inRex {
			return a.pane(c.Name, f.Namespace, false)
		}
		return rex.Split("k8s", self, "pane", c.Name, nsOr(f.Namespace))
	}
	return nil
}

// describeTail: rex8s _describe CTX NS RESOURCE NAME — kubectl describe in
// this pane, cleaned of escape sequences, until enter.
func (a *app) describeTail(ctxName, ns, resource, name string) error {
	c, err := a.context(ctxName)
	if err != nil {
		return err
	}
	rex.Rename("describe " + c.Short + " " + name)
	a.tint(c)
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	args := []string{"describe", resource, name}
	if ns != "" {
		args = append(args, "-n", ns)
	}
	out, err := cl.Get(c.Name, args...)
	if err != nil {
		return errors.New(lastLogLine(err.Error()))
	}
	fmt.Print(tui.Clean(string(out)))
	waitClose("")
	return nil
}
