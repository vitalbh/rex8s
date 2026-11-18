package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/testkube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// Two clusters with names longer than the old 8-character guess: one in
// production.
var (
	kvProd = kube.Context{Name: "ctx-production-eu-west", Short: "production-eu-west", Prod: true}
	kvDev  = kube.Context{Name: "ctx-use", Short: "use"}
)

// fakeKview is a kview -A on kvProd and kvDev whose kubectl answers `get
// RESOURCE` with replies["<context>-<resource>.json"] ({"items":[]} when
// missing). Its kubectl is also where the guard would be (binDir).
func fakeKview(t *testing.T, replies map[string]string) *kview {
	t.Helper()
	k := testkube.New(t).Default(testkube.Reply{Stdout: `{"items":[]}`})
	for name, body := range replies {
		used := false
		for _, c := range []kube.Context{kvProd, kvDev} {
			if res, ok := strings.CutPrefix(strings.TrimSuffix(name, ".json"), c.Name+"-"); ok {
				k.On(c.Name, "get "+res+" *", testkube.Reply{Stdout: body})
				used = true
			}
		}
		if !used {
			t.Fatalf("fakeKview: %q names no cluster", name)
		}
	}
	r := &fleet.Reader{Client: kube.Client{Kubectl: k.Path, Kubeconfig: k.Kubeconfig, Timeout: 5 * time.Second}}
	return &kview{a: &app{binDir: k.Dir, src: k.Kubeconfig}, cl: r.Client, fleet: r,
		clusters: []kube.Context{kvProd, kvDev}, multi: true, objs: map[string]any{}, headers: map[string]string{}}
}

func colors(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(old) })
}

const kvPodsJSON = `{"items":[
 {"metadata":{"name":"web-7d9f","namespace":"default","labels":{"app":"web"}},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running"}},
 {"metadata":{"name":"web-admin-5c4b","namespace":"default","labels":{"app":"web-admin"}},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running"}}
]}`

const kvDeployJSON = `{"items":[{"kind":"Deployment","metadata":{"name":"web","namespace":"default"},
 "spec":{"replicas":1,"selector":{"matchLabels":{"app":"web"}}},"status":{"readyReplicas":1}}]}`

const kvJobsJSON = `{"items":[{"metadata":{"name":"migrate","namespace":"default","creationTimestamp":"2026-01-01T10:30:00Z"},
 "status":{"startTime":"2026-01-01T10:30:00Z","active":1}}]}`

const kvCronsJSON = `{"items":[{"metadata":{"name":"export","namespace":"default"},"spec":{"schedule":"0 * * * *"}}]}`

func load(t *testing.T, fn func(func([]tui.Item)) ([]tui.Item, error)) []tui.Item {
	t.Helper()
	items, err := fn(func([]tui.Item) {})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// rowOn: the item whose Name ends with "on SHORT…" for that object.
func rowOn(items []tui.Item, prefix string) (tui.Item, bool) {
	for _, it := range items {
		if strings.HasPrefix(it.Name, prefix) {
			return it, true
		}
	}
	return tui.Item{}, false
}

// The confirmation comes from the object, not from the -A label (B2): every
// tab with a Confirm action names kind, namespace/name, cluster and PROD.
func TestKviewNamesUnderA(t *testing.T) {
	colors(t)
	k := fakeKview(t, map[string]string{
		kvProd.Name + "-pods.json": kvPodsJSON, kvDev.Name + "-pods.json": kvPodsJSON,
		kvProd.Name + "-deployments,statefulsets,daemonsets.json": kvDeployJSON,
		kvProd.Name + "-jobs.json":                                kvJobsJSON, kvProd.Name + "-cronjobs.json": kvCronsJSON,
		kvDev.Name + "-jobs.json": kvJobsJSON,
	})
	prodCol := tui.Err.Render("production-eu-west")
	if !strings.Contains(prodCol, "\x1b[") {
		t.Fatal("no colors in tests: the red check would pass for nothing")
	}
	devCol := "use" + strings.Repeat(" ", len("production-eu-west")-len("use"))

	pods := load(t, podTab.loader(k))
	for _, want := range []string{"pod default/web-7d9f on production-eu-west (PROD)", "pod default/web-7d9f on use"} {
		if _, ok := rowOn(pods, want); !ok {
			t.Errorf("pods: no row named %q", want)
		}
	}
	if it, _ := rowOn(pods, "pod default/web-7d9f on use"); strings.Contains(it.Name, "PROD") {
		t.Errorf("a dev cluster's pod says PROD: %q", it.Name)
	}

	wls := load(t, workloadTab.loader(k))
	if _, ok := rowOn(wls, "deploy default/web on production-eu-west (PROD)"); !ok {
		t.Errorf("workloads: %v", names(wls))
	}

	jobs := load(t, jobTab.loader(k))
	it, ok := rowOn(jobs, "cronjob default/export on production-eu-west (PROD)")
	if !ok {
		t.Fatalf("jobs: %v", names(jobs))
	}
	if !strings.HasPrefix(it.Label, prodCol) {
		t.Errorf("jobs -A: production's cluster column is not red: %q", it.Label)
	}
	if it, _ := rowOn(jobs, "job default/migrate on use"); !strings.HasPrefix(it.Label, devCol+"  ") {
		t.Errorf("jobs -A: dev label %q", it.Label)
	}
	if p, _ := rowOn(pods, "pod default/web-7d9f on production-eu-west"); !strings.HasPrefix(p.Label, prodCol) {
		t.Errorf("pods -A: production's cluster column is not red: %q", p.Label)
	}
}

func names(items []tui.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

// enter on a workload shows its pods by selector, in its cluster and
// namespace: web's are not web-admin's.
func TestKviewWorkloadPods(t *testing.T) {
	k := fakeKview(t, map[string]string{
		kvProd.Name + "-pods.json": kvPodsJSON, kvDev.Name + "-pods.json": kvPodsJSON,
		kvProd.Name + "-deployments,statefulsets,daemonsets.json": kvDeployJSON,
	})
	pods := load(t, podTab.loader(k))
	wls := load(t, workloadTab.loader(k))
	r := k.action(vWorkloads, wls[0], "enter")
	if r.Goto != vPods+1 || r.Only == nil || r.Filter != "" {
		t.Fatalf("enter: %+v", r)
	}
	var got []string
	for _, it := range pods {
		if r.Only(it) {
			got = append(got, it.Name)
		}
	}
	if want := []string{"pod default/web-7d9f on production-eu-west (PROD)"}; !slices.Equal(got, want) {
		t.Fatalf("pods of deploy/web: %v, want %v", got, want)
	}
	if !strings.Contains(r.OnlyLabel, "deploy default/web on production-eu-west") {
		t.Fatalf("label %q", r.OnlyLabel)
	}
}

// Skipped clusters come back as dim rows with their backoff; when every
// cluster is skipped the load fails.
func TestKviewSkippedClusters(t *testing.T) {
	k := fakeKview(t, map[string]string{kvDev.Name + "-pods.json": kvPodsJSON})
	for _, v := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(v, "") // a proxy turns the probe off
	}
	k.clusters[0].Server = "https://127.0.0.1:1" // nothing listens: unreachable
	k.fleet.Probe = 500 * time.Millisecond
	var partials int
	items, err := podTab.loader(k)(func([]tui.Item) { partials++ })
	if err != nil {
		t.Fatal(err)
	}
	last := items[len(items)-1]
	if !last.Dim || last.Key != "" || !strings.Contains(last.Label, "skipped production-eu-west: unreachable") ||
		!strings.Contains(last.Label, "next try") {
		t.Fatalf("skipped row: %+v", last)
	}
	if partials != 2 {
		t.Fatalf("%d partial loads, want one per cluster", partials)
	}
	k.clusters = k.clusters[:1]
	if _, err := podTab.loader(k)(nil); err == nil {
		t.Fatal("every cluster skipped: want an error")
	}
}

// Under -A a cluster that has not answered yet keeps its previous rows.
func TestKviewPartialKeepsSlowClusters(t *testing.T) {
	k := fakeKview(t, map[string]string{kvProd.Name + "-pods.json": kvPodsJSON, kvDev.Name + "-pods.json": kvPodsJSON})
	fn := podTab.loader(k)
	load(t, fn)
	var first []tui.Item
	_, err := fn(func(items []tui.Item) {
		if first == nil {
			first = items
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 4 {
		t.Fatalf("the first partial has %d rows, want 4 (the other cluster's from before)", len(first))
	}
}

// The argv each change hands to the guard, and the reads, without kubectl.
func TestKviewActionArgv(t *testing.T) {
	t.Setenv("REX_BLOCK", "")
	k := fakeKview(t, nil)
	guard := filepath.Join(k.a.binDir, "kubectl")
	pod := kube.On[kube.Pod]{C: kvProd, V: kube.Pod{Namespace: "default", Name: "web-7d9f", Containers: []string{"app", "proxy"}}}
	wl := kube.On[kube.Workload]{C: kvProd, V: kube.Workload{Kind: "Deployment", Namespace: "default", Name: "web"}}
	cron := kube.CronJob{Namespace: "default", Name: "export"}
	fromCron := kube.Job{Namespace: "default", Name: "export-1", Cron: "export"}
	plain := kube.Job{Namespace: "default", Name: "migrate"}

	tail := func(r tui.Result) []string {
		t.Helper()
		if r.Exec == nil {
			t.Fatalf("no command: %+v", r)
		}
		a := r.Exec.Args
		if a[0] != "/bin/sh" || a[1] != "-c" || a[3] != guard {
			t.Fatalf("not through the guard: %q", a)
		}
		return a[4:]
	}
	cases := []struct {
		r    tui.Result
		want []string
	}{
		{k.podAction(pod, "ctrl+d"), []string{"--context", kvProd.Name, "-n", "default", "delete", "pod", "web-7d9f"}},
		{k.workloadAction(wl, "r"), []string{"--context", kvProd.Name, "-n", "default", "rollout", "restart", "deployment/web"}},
	}
	for _, c := range cases {
		if got := tail(c.r); !slices.Equal(got, c.want) {
			t.Errorf("argv %q, want %q", got, c.want)
		}
	}
	for _, r := range []tui.Result{k.jobAction(kvJob{c: kvProd, cron: &cron}, "r"), k.jobAction(kvJob{c: kvProd, job: &fromCron}, "r")} {
		got := tail(r)
		if len(got) != 8 || got[1] != kvProd.Name || got[3] != "default" || got[4] != "create" || got[5] != "job" ||
			!strings.HasPrefix(got[6], "export-manual-") || got[7] != "--from=cronjob/export" {
			t.Errorf("run now: %q", got)
		}
	}
	if r := k.jobAction(kvJob{c: kvProd, job: &plain}, "r"); r.Err == nil || r.Exec != nil {
		t.Errorf("a Job without a CronJob reran: %+v", r)
	}

	// outside Rex, logs run here: rex8s _logs CTX NS POD *
	r := k.podAction(pod, "l")
	if r.Exec == nil || !slices.Equal(r.Exec.Args[1:], []string{"_logs", kvProd.Name, "default", "web-7d9f", "*"}) {
		t.Errorf("logs: %+v", r)
	}

	// no guard installed: refuse instead of running kubectl directly
	os.Remove(guard)
	if r := k.podAction(pod, "ctrl+d"); r.Err == nil || r.Exec != nil {
		t.Errorf("without the guard: %+v", r)
	}
}

// describe/YAML text is cleaned of escape sequences.
func TestKviewTextIsCleaned(t *testing.T) {
	fake := testkube.New(t).Default(testkube.Reply{Stdout: "Name: web\x1b]52;c;cGF3bmVk\a\x1b[2J\nok\n"})
	k := &kview{cl: kube.Client{Kubectl: fake.Path, Kubeconfig: fake.Kubeconfig}}
	r := k.text(kvDev, "describe pod web", "describe", "pod", "web")
	if r.Err != nil || r.Text != "Name: web\nok\n" {
		t.Fatalf("text %q err %v", r.Text, r.Err)
	}
}

func TestKviewLabel(t *testing.T) {
	cases := []struct {
		cs   []kube.Context
		all  bool
		ns   string
		want string
	}{
		{[]kube.Context{kvProd}, false, "payments", "kview PROD production-eu-west/payments"},
		{[]kube.Context{kvDev}, false, "", "kview use"},
		{[]kube.Context{kvDev}, false, "payments", "kview use/payments"},
		{[]kube.Context{kvDev, kvProd}, true, "", "kview PROD all"},
		{[]kube.Context{kvDev}, true, "", "kview all"},
	}
	for _, c := range cases {
		if got := kviewLabel(c.cs, c.all, c.ns); got != c.want {
			t.Errorf("kviewLabel = %q, want %q", got, c.want)
		}
	}
}

func TestMatchLabels(t *testing.T) {
	l := map[string]string{"app": "web", "tier": "front"}
	for sel, want := range map[string]bool{"app=web": true, "app=web,tier=front": true, "app=web-admin": false, "app=web,x=y": false, "": false} {
		if got := matchLabels(l, sel); got != want {
			t.Errorf("matchLabels(%q) = %v", sel, got)
		}
	}
}
