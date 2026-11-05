package main

import (
	"os"
	"strings"
	"testing"
	"time"

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
