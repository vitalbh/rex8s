package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/kube"
)

func TestImageTag(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/acme/api:1.4.2": "1.4.2",
		"nginx":                  "latest",
		"localhost:5000/web":     "latest",
		"localhost:5000/web:2.0": "2.0",
		"ghcr.io/acme/api@sha256:0123456789abcdef0123": "@0123456789ab",
		"": "?",
	}
	for in, want := range cases {
		if got := imageTag(in); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVersionRows(t *testing.T) {
	use := kube.Context{Short: "use", Prod: true}
	stg := kube.Context{Short: "staging"}
	w := func(c kube.Context, name, img string, extra ...containerImage) target {
		return target{c: c, Kind: "Deployment", Namespace: "app", Name: name,
			Containers: append(extra, containerImage{name, img})}
	}
	rows := versionRows([]target{
		w(use, "web", "acme/web:3.8.1"), w(stg, "web", "acme/web:3.8.1"),
		// the sidecar comes first; the main container is the one named like the workload
		w(use, "api", "acme/api:1.42.0", containerImage{"proxy", "envoy:1.30"}), w(stg, "api", "acme/api:1.43.0"),
	})
	if len(rows) != 2 || rows[0].Name != "api" || !rows[0].differs() || rows[1].differs() {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Tags["use"] != "1.42.0" || rows[0].Tags["staging"] != "1.43.0" {
		t.Errorf("api tags = %v", rows[0].Tags)
	}
	out := renderVersions(rows, []kube.Context{use, stg})
	if !strings.Contains(out, "1 differ") {
		t.Errorf("summary missing:\n%s", out)
	}
}

func TestCleanSpec(t *testing.T) {
	in := `{"apiVersion":"apps/v1","kind":"Deployment",
	  "metadata":{"name":"api","uid":"x","resourceVersion":"9","generation":4,"creationTimestamp":"2026-01-01T00:00:00Z",
	    "managedFields":[{}],"annotations":{"deployment.kubernetes.io/revision":"7"},"labels":{"app":"api"}},
	  "spec":{"replicas":3,"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"now"}},
	    "spec":{"containers":[{"name":"api","image":"acme/api:1"}]}}},
	  "status":{"readyReplicas":3}}`
	out, err := cleanSpec([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, gone := range []string{"uid", "resourceVersion", "generation", "managedFields", "revision", "restartedAt", "status", "readyReplicas", "annotations"} {
		if strings.Contains(s, gone) {
			t.Errorf("%q still in:\n%s", gone, s)
		}
	}
	for _, kept := range []string{"replicas: 3", "image: acme/api:1", "app: api"} {
		if !strings.Contains(s, kept) {
			t.Errorf("%q missing from:\n%s", kept, s)
		}
	}
}

func TestUnifiedDiff(t *testing.T) {
	out, same, err := unifiedDiff("a", []byte("x: 1\n"), "b", []byte("x: 2\n"))
	if err != nil || same || !strings.Contains(out, "-x: 1") || !strings.Contains(out, "+x: 2") {
		t.Fatalf("diff = %q same=%v err=%v", out, same, err)
	}
	if _, same, err := unifiedDiff("a", []byte("x\n"), "b", []byte("x\n")); err != nil || !same {
		t.Fatalf("same=%v err=%v", same, err)
	}
}

func TestParseNodes(t *testing.T) {
	in := `{"items":[
	  {"metadata":{"name":"b","labels":{"node-role.kubernetes.io/worker":""}},"status":{"conditions":[{"type":"Ready","status":"True"}],"nodeInfo":{"kubeletVersion":"v1.30.4"}}},
	  {"metadata":{"name":"c"},"status":{"conditions":[{"type":"Ready","status":"True"},{"type":"MemoryPressure","status":"True"}]}},
	  {"metadata":{"name":"a"},"spec":{"unschedulable":true},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}`
	ns, err := parseNodes([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, n := range ns {
		got = append(got, n.Name+":"+n.badge())
	}
	if strings.Join(got, " ") != "a:DOWN c:PRES b:" {
		t.Errorf("nodes = %v", got)
	}
	if ns[2].Roles[0] != "worker" || !strings.Contains(ns[2].hint(), "v1.30.4") {
		t.Errorf("b = %+v", ns[2])
	}
}

func TestPfList(t *testing.T) {
	a := &app{cache: t.TempDir()}
	keep := a.pfRegister(pfEntry{PID: os.Getpid(), Service: "api", Local: 8080, Remote: 80, Started: time.Now()})
	a.pfRegister(pfEntry{PID: 999999, Service: "gone", Started: time.Now()}) // no such process
	es := a.pfList()
	if len(es) != 1 || es[0].Service != "api" || es[0].URL() != "http://localhost:8080" {
		t.Fatalf("list = %+v", es)
	}
	keep()
	if es := a.pfList(); len(es) != 0 {
		t.Fatalf("after remove: %+v", es)
	}
}

func TestSetYAMLKey(t *testing.T) {
	doc := "kubeconfig: ~/.kube/config\nprod_background: \"#2b1216\"   # tint\nhide: []\n"
	got := setYAMLKey(doc, "prod_background", `"#1f1430"`)
	if !strings.Contains(got, "prod_background: \"#1f1430\"") || !strings.Contains(got, "# tint") || !strings.Contains(got, "hide: []") || strings.Contains(got, "2b1216") {
		t.Errorf("replace:\n%s", got)
	}
	if got := setYAMLKey("hide: []", "prod_background", `"off"`); got != "hide: []\nprod_background: \"off\"\n" {
		t.Errorf("append: %q", got)
	}
	for in, want := range map[string]string{"#1F1430": "#1f1430", "1f1430": "#1f1430", "off": "off"} {
		if got, err := normTint(in); err != nil || got != want {
			t.Errorf("normTint(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := normTint("red"); err == nil {
		t.Error("normTint(red) should fail")
	}
}

func TestRolloutChanges(t *testing.T) {
	now := time.Now()
	rs := func(name string, rev int, ago time.Duration, img string) string {
		return fmt.Sprintf(`{"metadata":{"name":%q,"namespace":"api","creationTimestamp":%q,
		  "annotations":{"deployment.kubernetes.io/revision":"%d"},"ownerReferences":[{"kind":"Deployment","name":"api"}]},
		  "spec":{"template":{"spec":{"containers":[{"name":"api","image":%q}]}}}}`, name, now.Add(-ago).UTC().Format(time.RFC3339), rev, img)
	}
	data := `{"items":[` + rs("api-1", 11, 48*time.Hour, "acme/api:1.41.0") + "," + rs("api-2", 12, 20*time.Minute, "acme/api:1.42.0") + `]}`
	cs := rolloutChanges([]byte(data), now.Add(-time.Hour))
	if len(cs) != 1 || cs[0].what != "deploy api/api" || cs[0].detail != "rev 12 · api 1.41.0 → 1.42.0" {
		t.Fatalf("changes = %+v", cs)
	}
}

func TestFluxChanges(t *testing.T) {
	ts := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	ev := func(typ, kind, reason, msg string) string {
		b, _ := json.Marshal(msg)
		return fmt.Sprintf(`{"type":%q,"reason":%q,"message":%s,"involvedObject":{"kind":%q,"name":"apps","namespace":"flux-system"},"lastTimestamp":%q}`, typ, reason, b, kind, ts)
	}
	data := `{"items":[` + strings.Join([]string{
		ev("Normal", "Kustomization", "ReconciliationSucceeded", "Reconciliation finished in 2s"),
		ev("Normal", "Kustomization", "Progressing", "Deployment/api/api configured\nService/api/api unchanged"),
		ev("Normal", "HelmRelease", "UpgradeSucceeded", "Helm upgrade succeeded for release payments"),
		ev("Warning", "HelmRelease", "UpgradeFailed", "Helm upgrade failed: timeout"),
		ev("Normal", "GitRepository", "NewArtifact", "stored artifact for commit 'bump api'"),
		ev("Normal", "Pod", "Pulled", "pulled"),
	}, ",") + `]}`
	cs := fluxChanges([]byte(data), time.Now().Add(-time.Hour))
	got := []string{}
	for _, c := range cs {
		got = append(got, c.kind+":"+c.detail)
	}
	want := "flux:Deployment/api/api configured|helm:Helm upgrade succeeded for release payments|helm:Helm upgrade failed: timeout|git:stored artifact for commit 'bump api'"
	if strings.Join(got, "|") != want {
		t.Errorf("got  %s\nwant %s", strings.Join(got, "|"), want)
	}
}

func TestPodWorkload(t *testing.T) {
	for in, want := range map[string]string{
		"api-7d9f6c5b8-4kq2x":          "api",
		"payments-api-6f7c8d9b5-kz4lp": "payments-api",
		"redis-0":                      "redis",
		"node-exporter-7xk2m":          "node-exporter",
		"nightly-sync-29871540-mbk4c":  "nightly-sync-29871540",
	} {
		if got := podWorkload(in); got != want {
			t.Errorf("podWorkload(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewBroken(t *testing.T) {
	cs := []clusterFailingJSON{{Context: "use", Pods: []podJSON{{Namespace: "api", Name: "worker-5c8b7d4f9-hx2lp", Status: "CrashLoopBackOff"}}}}
	now := brokenKeys(cs)
	if got := newBroken(nil, now); got != nil {
		t.Errorf("first run should not notify: %v", got)
	}
	if got := newBroken(map[string]bool{}, now); len(got) != 1 || now[got[0]] != "api/worker CrashLoopBackOff" {
		t.Errorf("new = %v", got)
	}
	// the crash-looping pod recreated under a new name: same workload, not new
	cs[0].Pods[0].Name = "worker-5c8b7d4f9-zz9qq"
	seen := map[string]bool{}
	for k := range now {
		seen[k] = true
	}
	if got := newBroken(seen, brokenKeys(cs)); len(got) != 0 {
		t.Errorf("recreated pod notified again: %v", got)
	}
}

func TestGrepLine(t *testing.T) {
	re, err := logOpts{grep: "time ?out"}.compile()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := grepLine("GET /health 200", re); ok {
		t.Error("non-matching line kept")
	}
	if l, ok := grepLine("upstream TIMEOUT after 30s", re); !ok || !strings.Contains(l, "TIMEOUT") {
		t.Errorf("match = %q %v", l, ok)
	}
	if l, ok := grepLine("x", nil); !ok || l != "x" {
		t.Error("no pattern should pass everything")
	}
	rest, lo := splitLogFlags([]string{"--grep=err", "ctx", "--since=1h", "ns", "pod"})
	if strings.Join(rest, " ") != "ctx ns pod" || lo.grep != "err" || lo.since != "1h" {
		t.Errorf("split = %v %+v", rest, lo)
	}
	if _, err := (logOpts{grep: "("}).compile(); err == nil {
		t.Error("bad regex accepted")
	}
}

func TestCron(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.Parse("2006-01-02 15:04", s); return v }
	cases := []struct {
		expr, tz, from string
		want           []string
	}{
		{"*/15 * * * *", "", "2026-11-05 10:07", []string{"10:15", "10:30", "10:45"}},
		{"0 3 * * *", "", "2026-11-05 10:00", []string{"03:00"}},
		{"30 9 * * MON-FRI", "", "2026-11-06 10:00", []string{"09:30"}}, // Fri 10:00 → Mon 09:30
		{"@hourly", "", "2026-11-05 10:30", []string{"11:00", "12:00"}},
		{"0 6 * * *", "America/Sao_Paulo", "2026-11-05 00:00", []string{"09:00"}}, // 06:00 BRT = 09:00 UTC
	}
	for _, c := range cases {
		s, err := parseCron(c.expr, c.tz)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		got := s.runs(at(c.from), at(c.from).Add(72*time.Hour), len(c.want))
		var hm []string
		for _, g := range got {
			hm = append(hm, g.UTC().Format("15:04"))
		}
		if strings.Join(hm, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s from %s = %v, want %v", c.expr, c.from, hm, c.want)
		}
	}
	if s, _ := parseCron("30 9 * * MON-FRI", ""); s.runs(at("2026-11-06 10:00"), at("2026-11-09 23:59"), 1)[0].Weekday() != time.Monday {
		t.Error("weekday range")
	}
	for _, bad := range []string{"* * *", "61 * * * *", "*/0 * * * *"} {
		if _, err := parseCron(bad, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestKreq(t *testing.T) {
	for in, want := range map[string]int64{"250m": 250, "2": 2000, "0.5": 500, "1500000n": 1, "": 0} {
		if got := parseCPU(in); got != want {
			t.Errorf("parseCPU(%q) = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[string]int64{"256Mi": 256 << 20, "1Gi": 1 << 30, "512M": 512e6, "1000": 1000} {
		if got := parseMem(in); got != want {
			t.Errorf("parseMem(%q) = %d, want %d", in, got, want)
		}
	}
	pods := `{"items":[
	 {"metadata":{"name":"api-7d9f6c5b8-4kq2x","namespace":"app"},"status":{"phase":"Running"},"spec":{"containers":[
	   {"name":"api","resources":{"requests":{"cpu":"1","memory":"1Gi"},"limits":{"memory":"1Gi"}}}]}},
	 {"metadata":{"name":"search-0","namespace":"app"},"status":{"phase":"Running"},"spec":{"containers":[
	   {"name":"search","resources":{"requests":{"memory":"2Gi"},"limits":{"memory":"2Gi"}}}]}},
	 {"metadata":{"name":"tool-x7k2p","namespace":"app"},"status":{"phase":"Running"},"spec":{"containers":[{"name":"tool"}]}}]}`
	top := "app api-7d9f6c5b8-4kq2x api 50m 100Mi\napp search-0 search 300m 1900Mi\napp tool-x7k2p tool 5m 20Mi\n"
	ws, err := workloadResources([]byte(pods), []byte(top), true, "")
	if err != nil || len(ws) != 3 {
		t.Fatalf("ws = %+v, %v", ws, err)
	}
	out := renderKreq(ws, "test")
	for _, want := range []string{"app/search", "OOM risk", "app/api", "app/tool"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing:\n%s", want, out)
		}
	}
}

func TestAddYAMLListItem(t *testing.T) {
	item := "{ name: grafana, cluster: dal, namespace: monitoring, service: grafana, port: 3000 }"
	cases := map[string]string{
		"":                        "forwards:\n  - " + item + "\n",
		"hide: []\n":              "hide: []\nforwards:\n  - " + item + "\n",
		"forwards: []\nhide: x\n": "forwards:\n  - " + item + "\nhide: x\n",
		"forwards:\n  - { name: a }\n# note\nhide: x\n": "forwards:\n  - { name: a }\n  - " + item + "\n# note\nhide: x\n",
	}
	for in, want := range cases {
		if got := addYAMLListItem(in, "forwards", item); got != want {
			t.Errorf("addYAMLListItem(%q)\n got %q\nwant %q", in, got, want)
		}
	}
	if yamlScalar("grafana") != "grafana" || yamlScalar("a: b") != `"a: b"` {
		t.Error("yamlScalar")
	}
}

func TestStatsSummary(t *testing.T) {
	data := `{"node":{"fs":{"usedBytes":80,"capacityBytes":100}},"pods":[
	  {"podRef":{"name":"mysql-0","namespace":"db"},"volume":[
	    {"name":"data","usedBytes":90,"capacityBytes":100,"pvcRef":{"name":"data-mysql-0","namespace":"db"}},
	    {"name":"tmp","usedBytes":1,"capacityBytes":100}]}]}`
	ds := parseStatsSummary([]byte(data), "node-a")
	if len(ds) != 2 || ds[0].kind != "node" || ds[1].name != "db/data-mysql-0" || ds[1].pod != "mysql-0" {
		t.Fatalf("ds = %+v", ds)
	}
	// a local-path PVC reports the node's disk: folded under the node
	local := `{"node":{"fs":{"usedBytes":80,"capacityBytes":100}},"pods":[{"podRef":{"name":"solr-0","namespace":"s"},"volume":[
	  {"usedBytes":80,"capacityBytes":100,"pvcRef":{"name":"solr-data","namespace":"s"}}]}]}`
	if ls := parseStatsSummary([]byte(local), "n"); len(ls) != 1 || len(ls[0].shared) != 1 || ls[0].shared[0] != "s/solr-data" {
		t.Errorf("local-path not folded: %+v", ls)
	}
	out := renderDisks(ds, false, false)
	if !strings.Contains(out, "db/data-mysql-0") || !strings.Contains(out, "90%") {
		t.Errorf("render:\n%s", out)
	}
}

func TestKgitHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:Acme/GitOps.git":            "github.com/acme/gitops",
		"https://github.com/Acme/GitOps":            "github.com/acme/gitops",
		"ssh://git@github.com/Acme/GitOps.git":      "github.com/acme/gitops",
		"ssh://git@gitlab.example.com:2222/a/b.git": "gitlab.example.com/a/b",
	} {
		if got := normRepo(in); got != want {
			t.Errorf("normRepo(%q) = %q, want %q", in, got, want)
		}
	}
	dir := t.TempDir()
	os.MkdirAll(dir+"/clusters/dal/apps", 0o755)
	os.WriteFile(dir+"/clusters/dal/apps/api.yaml", []byte("apiVersion: v1\nkind: Service\nmetadata:\n  name: api\n---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n  namespace: app\n"), 0o644)
	hits := findDefinitions(dir, fluxChain{path: "./clusters/dal", lookKind: "Deployment", lookName: "api"})
	if len(hits) != 1 || hits[0].rel != "clusters/dal/apps/api.yaml" || hits[0].line != 9 {
		t.Fatalf("hits = %+v", hits)
	}
	if u := webURL("git@github.com:org/gitops.git", "main", "clusters/dal/apps/api.yaml", false); u != "https://github.com/org/gitops/blob/main/clusters/dal/apps/api.yaml" {
		t.Errorf("webURL = %s", u)
	}
}

func TestJunk(t *testing.T) {
	now := time.Now()
	ts := func(ago time.Duration) string { return now.Add(-ago).UTC().Format(time.RFC3339) }
	pods := fmt.Sprintf(`{"items":[
	  {"metadata":{"name":"node-debugger-n1-abcde","namespace":"default","creationTimestamp":%q},"status":{"phase":"Succeeded"}},
	  {"metadata":{"name":"knet-123","namespace":"api","creationTimestamp":%q},"status":{"phase":"Running"}},
	  {"metadata":{"name":"web-1","namespace":"web","creationTimestamp":%q},"status":{"phase":"Failed","reason":"Evicted"}},
	  {"metadata":{"name":"web-2","namespace":"web","creationTimestamp":%q},"status":{"phase":"Failed","reason":"Evicted"}},
	  {"metadata":{"name":"job-x","namespace":"web","creationTimestamp":%q,"ownerReferences":[{"kind":"Job"}]},"status":{"phase":"Failed"}}]}`,
		ts(184*24*time.Hour), ts(10*time.Minute), ts(3*24*time.Hour), ts(2*time.Hour), ts(9*24*time.Hour))
	ps := podJunk([]byte(pods), now, true, true)
	got := []string{}
	for _, j := range ps {
		got = append(got, j.cat+":"+j.name)
	}
	if strings.Join(got, " ") != "debug:node-debugger-n1-abcde pod:web-1" {
		t.Errorf("pods = %v", got)
	}
	jobs := fmt.Sprintf(`{"items":[
	  {"metadata":{"name":"old","namespace":"a"},"status":{"completionTime":%q}},
	  {"metadata":{"name":"recent","namespace":"a"},"status":{"completionTime":%q}},
	  {"metadata":{"name":"cron-1","namespace":"a","ownerReferences":[{"kind":"CronJob"}]},"status":{"completionTime":%q}},
	  {"metadata":{"name":"ttl","namespace":"a"},"spec":{"ttlSecondsAfterFinished":60},"status":{"completionTime":%q}}]}`,
		ts(30*24*time.Hour), ts(24*time.Hour), ts(30*24*time.Hour), ts(30*24*time.Hour))
	if js := jobJunk([]byte(jobs), now); len(js) != 1 || js[0].name != "old" {
		t.Errorf("jobs = %+v", js)
	}
	pvs := `{"items":[{"metadata":{"name":"pv-1"},"spec":{"persistentVolumeReclaimPolicy":"Retain","capacity":{"storage":"10Gi"},"claimRef":{"name":"data","namespace":"db"}},"status":{"phase":"Released"}},
	  {"metadata":{"name":"pv-2"},"spec":{},"status":{"phase":"Bound"}}]}`
	if vs := pvJunk([]byte(pvs), now); len(vs) != 1 || !strings.Contains(vs[0].why, "Retain") {
		t.Errorf("pvs = %+v", vs)
	}
}

func TestConfigEdits(t *testing.T) {
	doc := "ai_language: pt-BR\ngitops_repos: [ ~/dev/GitOps ]   # repos\nhide:\n  - \"old\"\n  - other   # note\nlogin_url: x\n"
	got := addYAMLFlowItem(doc, "gitops_repos", "~/dev/Infra")
	if !strings.Contains(got, "gitops_repos: [ ~/dev/GitOps, ~/dev/Infra ]   # repos") {
		t.Errorf("add flow:\n%s", got)
	}
	if addYAMLFlowItem(got, "gitops_repos", "~/dev/Infra") != got {
		t.Error("add flow duplicated")
	}
	if got := removeYAMLListItem(doc, "gitops_repos", "~/dev/GitOps"); !strings.Contains(got, "gitops_repos: [  ]") && !strings.Contains(got, "gitops_repos: [ ]") {
		t.Errorf("remove flow:\n%s", got)
	}
	if got := removeYAMLListItem(doc, "hide", "other"); strings.Contains(got, "other") || !strings.Contains(got, `"old"`) || !strings.Contains(got, "login_url: x") {
		t.Errorf("remove block:\n%s", got)
	}
	if got := removeYAMLKey(doc, "hide"); strings.Contains(got, "hide") || strings.Contains(got, "old") || !strings.Contains(got, "login_url: x") {
		t.Errorf("unset:\n%s", got)
	}
}

func TestEditConfigRefusesConcurrentChange(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.yaml"
	os.WriteFile(path, []byte("ai_language: en\n"), 0o644)
	t.Setenv("REX8S_CONFIG", path)
	cfg, _ := config.Load()
	a := &app{cfg: cfg}
	if err := a.editConfig(func(doc string) string { return setYAMLKey(doc, "ai_language", "pt-BR") }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "ai_language: pt-BR\n" {
		t.Errorf("file = %q", b)
	}
	// the user saves the file while rex8s is editing it
	err := a.editConfig(func(doc string) string {
		future := time.Now().Add(time.Hour)
		os.Chtimes(path, future, future)
		return setYAMLKey(doc, "ai_language", "en")
	})
	if err == nil || !strings.Contains(err.Error(), "changed while") {
		t.Errorf("concurrent edit not refused: %v", err)
	}
	// a change that breaks the YAML is undone
	before, _ := os.ReadFile(path)
	if err := a.editConfig(func(doc string) string { return doc + "clusters: [ {\n" }); err == nil {
		t.Error("broken YAML accepted")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("not restored: %q", after)
	}
}
