package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/testkube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kvFake is a kview -A on kvProd and kvDev with its fake kubectl, for the
// tests that set answers other than `get`.
func kvFake(t *testing.T) (*kview, *testkube.Kubectl) {
	t.Helper()
	f := testkube.New(t).Default(testkube.Reply{Stdout: `{"items":[]}`})
	r := &fleet.Reader{Client: kube.Client{Kubectl: f.Path, Kubeconfig: f.Kubeconfig, Timeout: 5 * time.Second}}
	fakeGuard(t, f.Dir)
	k := newKview(&app{binDir: f.Dir, src: f.Kubeconfig, cfg: &config.Config{}}, r)
	k.clusters, k.multi = []kube.Context{kvProd, kvDev}, true
	k.out, k.pause = &bytes.Buffer{}, func() {}
	return k, f
}

func onlyNames(items []tui.Item, only func(tui.Item) bool) []string {
	var out []string
	for _, it := range items {
		if only(it) {
			out = append(out, it.Name)
		}
	}
	return out
}

func TestParseKviewArgs(t *testing.T) {
	cases := []struct {
		args     []string
		clusters []string
		start    int
		err      bool
	}{
		{[]string{"-A"}, nil, vPods, false},
		{[]string{"-A", "use,euw"}, []string{"use", "euw"}, vPods, false},
		{[]string{"-A", "prod", "nodes"}, []string{"prod"}, vNodes, false},
		{[]string{"-A", "use", "euw", "svc"}, []string{"use", "euw"}, vServices, false},
		{[]string{"use"}, nil, 0, true},                    // names only with -A
		{[]string{"-A", "--context", "use"}, nil, 0, true}, // one or the other
	}
	for _, c := range cases {
		o, err := parseKviewArgs(c.args)
		if (err != nil) != c.err {
			t.Errorf("%q: err %v", c.args, err)
			continue
		}
		if !c.err && (!slices.Equal(o.clusters, c.clusters) || o.start != c.start || !o.all) {
			t.Errorf("%q: %+v", c.args, o)
		}
	}
}

// -A GROUP expands the config's group; names and groups mix, each cluster
// once.
func TestKviewClustersGroups(t *testing.T) {
	dir := t.TempDir()
	kc := filepath.Join(dir, "kubeconfig")
	os.WriteFile(kc, []byte(`apiVersion: v1
kind: Config
clusters:
- {name: use, cluster: {server: "https://k8s.us-east.example.com"}}
- {name: euw, cluster: {server: "https://k8s.eu-west.example.com"}}
- {name: stg, cluster: {server: "https://k8s.staging.example.com"}}
users: [{name: u, user: {}}]
contexts:
- {name: ctx-use, context: {cluster: use, user: u}}
- {name: ctx-euw, context: {cluster: euw, user: u}}
- {name: ctx-stg, context: {cluster: stg, user: u}}
`), 0o600)
	a := &app{src: kc, cfg: &config.Config{
		Clusters: []config.Cluster{{Match: "us-east", Name: "use", Prod: true}, {Match: "eu-west", Name: "euw", Prod: true}, {Match: "staging", Name: "staging"}},
		Groups:   map[string][]string{"prod": {"use", "euw"}, "empty": {}},
	}}
	shorts := func(cs []kube.Context) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Short)
		}
		return out
	}
	cs, err := a.kviewClusters([]string{"prod", "staging", "use"})
	if err != nil || !slices.Equal(shorts(cs), []string{"use", "euw", "staging"}) {
		t.Fatalf("prod,staging,use → %v %v", shorts(cs), err)
	}
	if cs, _ := a.kviewClusters(nil); len(cs) != 3 {
		t.Fatalf("all: %v", shorts(cs))
	}
	if _, err := a.kviewClusters([]string{"nope"}); err == nil {
		t.Fatal("an unknown cluster was accepted")
	}
	if _, err := a.kviewClusters([]string{"empty"}); err == nil {
		t.Fatal("an empty group was accepted")
	}
}

const kvNavPodsJSON = `{"items":[
 {"metadata":{"name":"web-7d9f6c5b8-x2k4p","namespace":"default","labels":{"app":"web","pod-template-hash":"7d9f6c5b8"},
  "ownerReferences":[{"kind":"ReplicaSet","name":"web-7d9f6c5b8","controller":true}]},
  "spec":{"nodeName":"node-a1","containers":[{"name":"app"},{"name":"proxy"}]},"status":{"phase":"Running"}},
 {"metadata":{"name":"redis-0","namespace":"default","labels":{"app":"redis"},
  "ownerReferences":[{"kind":"StatefulSet","name":"redis","controller":true}]},
  "spec":{"nodeName":"node-b1","containers":[{"name":"redis"}]},"status":{"phase":"Running"}},
 {"metadata":{"name":"migrate-29871540-mbk4c","namespace":"default","labels":{"job-name":"migrate-29871540"},
  "ownerReferences":[{"kind":"Job","name":"migrate-29871540","controller":true}]},
  "spec":{"nodeName":"node-a1","containers":[{"name":"app"}]},"status":{"phase":"Succeeded"}},
 {"metadata":{"name":"bare","namespace":"default"},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running"}}
]}`

const kvNavWorkloadsJSON = `{"items":[
 {"kind":"Deployment","metadata":{"name":"web","namespace":"default"},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"web"}}},"status":{"readyReplicas":1}},
 {"kind":"StatefulSet","metadata":{"name":"redis","namespace":"default"},"spec":{"replicas":1,"selector":{"matchLabels":{"app":"redis"}}},"status":{"readyReplicas":1}}]}`

// enter follows links: pod → its workload (through the ReplicaSet), its
// Job; node → its pods; service → the pods it selects; warning → the
// object's tab.
func TestKviewNavigation(t *testing.T) {
	k, f := kvFake(t)
	for _, c := range []kube.Context{kvProd, kvDev} {
		f.On(c.Name, "get pods *", testkube.Reply{Stdout: kvNavPodsJSON})
		f.On(c.Name, "get deployments,statefulsets,daemonsets *", testkube.Reply{Stdout: kvNavWorkloadsJSON})
		f.On(c.Name, "get jobs *", testkube.Reply{Stdout: `{"items":[{"metadata":{"name":"migrate-29871540","namespace":"default"},"status":{}}]}`})
		f.On(c.Name, "get nodes *", testkube.Reply{Stdout: `{"items":[{"metadata":{"name":"node-a1"},"status":{}}]}`})
		f.On(c.Name, "get services *", testkube.Reply{Stdout: `{"items":[{"metadata":{"name":"web","namespace":"default"},
		 "spec":{"selector":{"app":"web"},"ports":[{"port":80}]}}, {"metadata":{"name":"ext","namespace":"default"},"spec":{"ports":[{"port":80}]}}]}`})
	}
	pods := load(t, podTab.loader(k))
	wls := load(t, workloadTab.loader(k))
	jobs := load(t, jobTab.loader(k))
	nodes := load(t, nodeTab.loader(k))
	svcs := load(t, serviceTab.loader(k))

	row := func(items []tui.Item, name string) tui.Item {
		t.Helper()
		it, ok := rowOn(items, name)
		if !ok {
			t.Fatalf("no row %q in %v", name, names(items))
		}
		return it
	}
	// pod → Deployment, without asking the cluster for the ReplicaSet
	r := k.action(vPods, row(pods, "pod default/web-7d9f6c5b8-x2k4p on use"), "enter")
	if r.Goto != vWorkloads+1 || !slices.Equal(onlyNames(wls, r.Only), []string{"deploy default/web on use"}) ||
		!strings.Contains(r.OnlyLabel, "owner of web-7d9f6c5b8-x2k4p") {
		t.Fatalf("pod → deployment: %+v %v", r, onlyNames(wls, r.Only))
	}
	if f.Count("replicaset") != 0 {
		t.Error("the pod-template-hash was enough: no need to read the ReplicaSet")
	}
	r = k.action(vPods, row(pods, "pod default/redis-0 on production-eu-west"), "enter")
	if !slices.Equal(onlyNames(wls, r.Only), []string{"sts default/redis on production-eu-west (PROD)"}) {
		t.Fatalf("pod → statefulset: %v", onlyNames(wls, r.Only))
	}
	r = k.action(vPods, row(pods, "pod default/migrate-29871540-mbk4c on use"), "enter")
	if r.Goto != vJobs+1 || !slices.Equal(onlyNames(jobs, r.Only), []string{"job default/migrate-29871540 on use"}) {
		t.Fatalf("pod → job: %v", onlyNames(jobs, r.Only))
	}
	if r = k.action(vPods, row(pods, "pod default/bare on use"), "enter"); r.Err == nil {
		t.Fatal("a pod without owner should say so")
	}
	// node → its pods, on its cluster only
	r = k.action(vNodes, row(nodes, "node node-a1 on use"), "enter")
	if r.Goto != vPods+1 || !slices.Equal(onlyNames(pods, r.Only), []string{"pod default/migrate-29871540-mbk4c on use", "pod default/web-7d9f6c5b8-x2k4p on use"}) {
		t.Fatalf("node → pods: %v", onlyNames(pods, r.Only))
	}
	// service → the pods of its selector; one without selector says so
	r = k.action(vServices, row(svcs, "svc default/web on use"), "o")
	if !slices.Equal(onlyNames(pods, r.Only), []string{"pod default/web-7d9f6c5b8-x2k4p on use"}) {
		t.Fatalf("svc → pods: %v", onlyNames(pods, r.Only))
	}
	if r = k.action(vServices, row(svcs, "svc default/ext on use"), "o"); r.Err == nil {
		t.Fatal("a service without selector should say so")
	}
	// warning → the pod's row
	k.keep(map[string]any{"warn|x": kvWarning{kvDev, "default", "Pod", "redis-0"}}, "warn|")
	r = k.action(vWarnings, tui.Item{Key: "warn|x"}, "enter")
	if r.Goto != vPods+1 || !slices.Equal(onlyNames(pods, r.Only), []string{"pod default/redis-0 on use"}) {
		t.Fatalf("warning → pod: %v", onlyNames(pods, r.Only))
	}
}

// A ReplicaSet without the hash label: kview asks it for its Deployment.
func TestKviewReplicaSetOwnerRead(t *testing.T) {
	k, f := kvFake(t)
	f.On(kvDev.Name, "get replicaset -n default web-abc -o json*", testkube.Reply{Stdout: `{"metadata":{"ownerReferences":[{"kind":"Deployment","name":"web"}]}}`})
	dep, err := k.replicaSetOwner(kvDev, kube.Pod{Namespace: "default", Name: "web-abc-x", Owner: kube.OwnerRef{Kind: "ReplicaSet", Name: "web-abc"}})
	if err != nil || dep != "web" {
		t.Fatalf("%q %v", dep, err)
	}
}

// A warning about a kind without a tab opens the ":KIND" tab on it.
func TestKviewWarningToResource(t *testing.T) {
	k, f := kvFake(t)
	f.On("*", "api-resources*", testkube.Reply{Stdout: "NAME        SHORTNAMES   APIVERSION             NAMESPACED   KIND\ningresses   ing          networking.k8s.io/v1   true         Ingress\n"})
	f.On("*", "get ingresses.networking.k8s.io -o wide -A*", testkube.Reply{Stdout: "NAMESPACE   NAME   CLASS   HOSTS             AGE\nweb         web    nginx   www.example.com   2d\nweb         api    nginx   api.example.com   2d\n"})
	r := k.warningAction(kvWarning{kvDev, "web", "Ingress", "web"}, "enter")
	if r.Open == nil || r.Open.Name != ":ingresses" {
		t.Fatalf("%+v", r)
	}
	items := load(t, r.Open.Load)
	if got := onlyNames(items, r.Only); !slices.Equal(got, []string{"ingress web/web on use"}) {
		t.Fatalf("only %v of %v", got, names(items))
	}
}

// ":KIND": the server's columns, a fixed tab for its kinds, an error for a
// kind the single cluster lacks, completion from api-resources.
func TestKviewResourceTab(t *testing.T) {
	k, f := kvFake(t)
	k.clusters, k.multi = []kube.Context{kvDev}, false
	f.On("*", "api-resources*", testkube.Reply{Stdout: "" +
		"NAME                       SHORTNAMES   APIVERSION                NAMESPACED   KIND\n" +
		"configmaps                 cm           v1                        true         ConfigMap\n" +
		"customresourcedefinitions  crd,crds     apiextensions.k8s.io/v1   false        CustomResourceDefinition\n"})
	f.On("*", "get configmaps -o wide -A*", testkube.Reply{Stdout: "" +
		"NAMESPACE   NAME         DATA   AGE\n" +
		"api         api-config   4      40d\n" +
		"web         nginx-conf   2      3h\n"})
	f.On("*", "get customresourcedefinitions.apiextensions.k8s.io -o wide*", testkube.Reply{Stdout: "NAME                  CREATED AT\nwidgets.example.com   2026-06-02T14:30:00Z\n"})

	if r := k.command(":po"); r.Goto != vPods+1 {
		t.Fatalf(":po → %+v", r)
	}
	if r := k.command("nope"); r.Err == nil {
		t.Fatal("an unknown kind on one cluster should fail")
	}
	if got := k.complete("cm"); len(got) == 0 || !strings.HasPrefix(got[0], "configmaps  (cm") {
		t.Fatalf("complete cm: %v", got)
	}
	if got := k.complete("map"); len(got) == 0 || !strings.HasPrefix(got[0], "configmaps") {
		t.Fatalf("complete map (anywhere in the name): %v", got)
	}
	if got := k.complete(""); len(got) < 4 {
		t.Fatalf("complete with nothing typed lists every kind: %v", got)
	}
	r := k.command("cm")
	if r.Open == nil || r.Open.Name != ":configmaps" {
		t.Fatalf(":cm → %+v", r)
	}
	items := load(t, r.Open.Load)
	if len(items) != 2 || items[0].Name != "configmap api/api-config on use" || !strings.Contains(items[0].Label, "api/api-config  4     40d") {
		t.Fatalf("rows %q %q", names(items), items[0].Label)
	}
	if h := r.Open.Header(); !strings.HasPrefix(h, "NAMESPACE/NAME  DATA  AGE") {
		t.Fatalf("header %q", h)
	}
	// sort by age: the youngest first
	age := k.sorts(sortAge)[0].Cmp
	if age(items[1], items[0]) >= 0 {
		t.Error("nginx-conf (3h) is younger than api-config (40d)")
	}
	t.Setenv("REX_BLOCK", "")
	guard := filepath.Join(k.a.binDir, "rex8s-guard")
	del := k.action(vResource, items[0], "ctrl+d")
	if del.Exec == nil || !slices.Equal(del.Exec.Args[3:], []string{guard, "kubectl", "--context", kvDev.Name, "-n", "api", "delete", "configmaps", "api-config"}) {
		t.Fatalf("delete %+v", del.Exec)
	}
	if y := k.action(vResource, items[0], "y"); y.Text == "" || f.Count("get configmaps -n api api-config -o yaml") != 1 {
		t.Fatalf("yaml: %+v %q", y, f.Calls())
	}

	// a cluster-scoped kind: no namespace, no -A
	r = k.command("crd")
	items = load(t, r.Open.Load)
	if len(items) != 1 || items[0].Name != "customresourcedefinition widgets.example.com on use" {
		t.Fatalf("crd rows %q", names(items))
	}
	if d := k.action(vResource, items[0], "d"); d.Err != nil || f.Count("describe customresourcedefinitions.apiextensions.k8s.io widgets.example.com") != 1 {
		t.Fatalf("describe: %+v %q", d, f.Calls())
	}
}

// Delete and restart of marked rows: one guard decision for all of them,
// then each change. An agent (no terminal) is refused on production.
func TestKviewActionMany(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir()) // no kunlock, the audit log out of the way
	t.Setenv("REX8S_FORCE", "")
	k, f := kvFake(t)
	f.On("*", "get pods *", testkube.Reply{Stdout: kvPodsJSON})
	f.On("*", "*delete*", testkube.Reply{Stdout: "pod deleted"})
	f.On("*", "*rollout restart*", testkube.Reply{Stdout: "restarted"})
	pods := load(t, podTab.loader(k))
	var dev []tui.Item
	for _, it := range pods {
		if strings.HasSuffix(it.Name, "on use") {
			dev = append(dev, it)
		}
	}
	changes, verb, err := k.plan(dev, "ctrl+d")
	if err != nil || verb != "delete" || len(changes) != 2 ||
		!slices.Equal(changes[0].args, []string{"-n", "default", "delete", "pod", "web-7d9f", "--wait=false"}) {
		t.Fatalf("plan %+v %v", changes, err)
	}
	if _, _, err := k.plan(dev, "l"); err == nil {
		t.Fatal("logs do not take marked rows")
	}
	r := k.actionMany(vPods, dev, "ctrl+d")
	if r.Run == nil {
		t.Fatalf("%+v", r)
	}
	_ = r.Run()
	if f.Count("delete pod") != 2 || !strings.Contains(k.out.(*bytes.Buffer).String(), "pod default/web-admin-5c4b on use") {
		t.Fatalf("dev: %d deletes, out %q", f.Count("delete pod"), k.out)
	}

	// with a production pod among them: refused (tests have no terminal)
	k.out = &bytes.Buffer{}
	_ = k.actionMany(vPods, pods, "ctrl+d").Run()
	if f.Count("delete pod") != 2 || !strings.Contains(k.out.(*bytes.Buffer).String(), "refused") {
		t.Fatalf("prod: %d deletes, out %q", f.Count("delete pod"), k.out)
	}

	// restart of marked workloads
	k.keep(map[string]any{"wl|a": kube.On[kube.Workload]{C: kvDev, V: kube.Workload{Kind: "Deployment", Namespace: "default", Name: "web"}}}, "wl|")
	changes, _, _ = k.plan([]tui.Item{{Key: "wl|a", Name: "deploy default/web on use"}}, "r")
	if len(changes) != 1 || !slices.Equal(changes[0].args, []string{"-n", "default", "rollout", "restart", "deployment/web"}) {
		t.Fatalf("restart plan %+v", changes)
	}
}

// Pods with several containers ask which; p shows the previous logs.
func TestKviewContainersAndPreviousLogs(t *testing.T) {
	t.Setenv("REX_BLOCK", "")
	k, f := kvFake(t)
	f.On(kvDev.Name, "logs -n default web *--previous*", testkube.Reply{Stdout: "panic: boom\n"})
	pod := kube.On[kube.Pod]{C: kvDev, V: kube.Pod{Namespace: "default", Name: "web", Containers: []string{"app", "proxy"}}}
	r := k.podAction(pod, "s")
	if r.Choose == nil || !slices.Equal(r.Choose.Options, []string{"app", "proxy"}) {
		t.Fatalf("shell: %+v", r)
	}
	if r = r.Choose.Then(1); r.Exec == nil || !slices.Equal(r.Exec.Args[1:], []string{"_exec", kvDev.Name, "default", "web", "proxy"}) {
		t.Fatalf("shell in proxy: %+v", r.Exec)
	}
	r = k.podAction(pod, "p").Choose.Then(0)
	if !strings.Contains(r.Text, "panic: boom") || !strings.Contains(r.TextTitle, "previous logs of web (app)") ||
		f.Count("logs -n default web --previous --tail=1000 -c app") != 1 {
		t.Fatalf("previous: %+v %q", r, f.Calls())
	}
	one := kube.On[kube.Pod]{C: kvDev, V: kube.Pod{Namespace: "default", Name: "solo", Containers: []string{"app"}}}
	if r := k.podAction(one, "l"); r.Choose != nil || r.Exec == nil {
		t.Fatal("one container: no question")
	}
}

// CPU and memory from kubectl top; without metrics-server, dashes.
func TestKviewMetrics(t *testing.T) {
	k, f := kvFake(t)
	k.metrics = newKvMetrics(k.cl)
	k.metrics.sync = true
	f.On("*", "get pods *", testkube.Reply{Stdout: kvPodsJSON})
	f.On(kvDev.Name, "top pods -A --no-headers", testkube.Reply{Stdout: "default   web-7d9f   12m   34Mi\n"})
	f.On(kvProd.Name, "top pods*", testkube.Reply{Stderr: "error: Metrics API not available", Exit: 1})
	pods := load(t, podTab.loader(k))
	dev, _ := rowOn(pods, "pod default/web-7d9f on use")
	prod, _ := rowOn(pods, "pod default/web-7d9f on production-eu-west")
	if !strings.Contains(dev.Label, "12m    34Mi") || !strings.Contains(prod.Label, "-      -") {
		t.Fatalf("dev %q prod %q", dev.Label, prod.Label)
	}
	load(t, podTab.loader(k))
	if n := f.Count("top pods"); n != 2 {
		t.Fatalf("top ran %d times, want once per cluster (every %s at most)", n, metricsEvery)
	}
	cpu := k.sorts(sortCPU)[0].Cmp
	if cpu(prod, dev) >= 0 {
		t.Error("no metrics sorts below some")
	}
	nodes := parseTopNodes("node-a1   250m   3%   1200Mi   10%\nnode-b1   <unknown>   <unknown>   <unknown>   <unknown>\n")
	if u := nodes["node-a1"]; u.cpu != "250m" || u.memPct != "10%" || u.memB != 1200<<20 || len(nodes) != 1 {
		t.Fatalf("top nodes %+v", nodes)
	}
}

// The Rex tab's status: failing pods, an expired login, idle when fine.
func TestKviewTabStatus(t *testing.T) {
	k, f := kvFake(t)
	var sent []string
	k.tab = &tabStatus{send: func(state, kind, msg string) { sent = append(sent, state+"|"+kind+"|"+msg) }, stop: func() {}}
	f.On(kvDev.Name, "get pods *", testkube.Reply{Stdout: `{"items":[{"metadata":{"name":"w","namespace":"d"},"spec":{"containers":[{"name":"a"}]},
	 "status":{"phase":"Running","containerStatuses":[{"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}]}`})
	f.On(kvProd.Name, "*", testkube.Reply{Stderr: "error: You must be logged in to the server (Unauthorized)", Exit: 1})
	load(t, podTab.loader(k))
	if len(sent) == 0 || sent[len(sent)-1] != "blocked|auth|kview: 1 failing · login expired: production-eu-west" {
		t.Fatalf("sent %q", sent)
	}
	f.On(kvProd.Name, "*", testkube.Reply{Stdout: `{"items":[]}`})
	f.On(kvDev.Name, "get pods *", testkube.Reply{Stdout: `{"items":[]}`})
	load(t, podTab.loader(k))
	if sent[len(sent)-1] != "idle||kview: no failing pods" {
		t.Fatalf("sent %q", sent)
	}
	n := len(sent)
	load(t, podTab.loader(k))
	if len(sent) != n {
		t.Fatal("the same status was sent again")
	}
	k.cleanup()
	if sent[len(sent)-1] != "clear||" {
		t.Fatalf("exit: %q", sent)
	}
}

// In Rex, a second logs (or shell, why) replaces the first one's split;
// kview's end closes what it opened.
func TestKviewSplits(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	os.WriteFile(filepath.Join(dir, "rex"), []byte(`#!/bin/sh
echo "$*" >> `+log+`
case "$1" in split) n=$(grep -c '^split' `+log+`); echo '{"block_ids":["new'$n'"]}';; esac
`), 0o755)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("REX_BLOCK", "self")
	t.Setenv("REX_SESSION", "s1")
	k, _ := kvFake(t)
	pod := kube.On[kube.Pod]{C: kvDev, V: kube.Pod{Namespace: "default", Name: "web", Containers: []string{"app"}}}
	other := kube.On[kube.Pod]{C: kvDev, V: kube.Pod{Namespace: "default", Name: "api", Containers: []string{"app"}}}
	if r := k.podAction(pod, "l"); r.Err != nil || !strings.Contains(r.Status, "opened in a split") {
		t.Fatalf("first logs: %+v", r)
	}
	if r := k.podAction(other, "l"); r.Err != nil || !strings.Contains(r.Status, "in the logs split") {
		t.Fatalf("second logs: %+v", r)
	}
	k.podAction(other, "a") // ask: a split of its own
	k.podAction(pod, "a")
	k.cleanup()
	data, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := []string{
		"split -s s1 --shell=none --json --label logs web --split right --ratio 50 --focus=false -b self --",
		"split -s s1 --shell=none --json --label logs api --split right --ratio 50 --focus=false -b new1 --", // from the first split…
		"--timeout 5s block close -s s1 new1", // …which then closes
		"split -s s1 --shell=none --json --label ask api --split right --ratio 50 --focus=true -b self --",
		"split -s s1 --shell=none --json --label ask web --split right --ratio 50 --focus=true -b self --",
		"--timeout 5s block close -s s1 new2",
		"--timeout 5s block close -s s1 new3",
		"--timeout 5s block close -s s1 new4",
	}
	if len(lines) != len(want) {
		t.Fatalf("rex calls:\n%s", data)
	}
	for i, w := range want {
		if !strings.HasPrefix(lines[i], w) {
			t.Errorf("call %d: %q, want %q…", i, lines[i], w)
		}
	}
}
