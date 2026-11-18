package main

import (
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/testkube"
)

func TestFindMatch(t *testing.T) {
	for _, c := range []struct {
		ns, name, q string
		exact, want bool
	}{
		{"api", "api-7d9f", "api", false, true},
		{"api", "API-7d9f", "api", false, true}, // case-insensitive
		{"api", "api-7d9f", "api", true, false},
		{"api", "api", "api", true, true},
		{"web", "frontend", "api", false, false}, // the namespace alone doesn't match
		{"api", "web", "api/web", true, true},
		{"api", "web-1", "api/web", false, true},
		{"web", "web-1", "api/web", false, false},
	} {
		if got := findMatch(c.ns, c.name, c.q, c.exact); got != c.want {
			t.Errorf("findMatch(%q, %q, %q, exact=%v) = %v", c.ns, c.name, c.q, c.exact, got)
		}
	}
}

// The queries print metadata columns only (never a Secret's data), and
// only the core kinds are required.
func TestFindQueries(t *testing.T) {
	qs := findQueries()
	if len(qs) != 3 {
		t.Fatalf("queries: %v", qs)
	}
	core := strings.Join(qs[0], " ")
	if !strings.HasPrefix(core, "get pods,deployments.apps,") || strings.Contains(core, "secrets") || strings.Contains(core, "fluxcd") {
		t.Errorf("core query: %s", core)
	}
	for _, q := range qs {
		s := strings.Join(q, " ")
		if strings.Contains(s, "-o json") || strings.Contains(s, "yaml") || !strings.Contains(s, "custom-columns=KIND:.kind,NAMESPACE:.metadata.namespace,NAME:.metadata.name,CREATED:.metadata.creationTimestamp") {
			t.Errorf("query prints more than metadata: %s", s)
		}
	}
}

// Read through fleet as kfind does: a cluster without Flux and with
// secrets forbidden still answers; one whose login expired is skipped.
func TestFindAcrossClusters(t *testing.T) {
	core := "Pod   api   api-7d9f6c5b8-4kq2x   2026-11-17T10:00:00Z\n" +
		"Deployment   api   api   2026-01-02T00:00:00Z\n" +
		"Service   web   frontend   2026-01-02T00:00:00Z\n" +
		"ConfigMap   api   api-config   2026-01-02T00:00:00Z\n"
	k := testkube.New(t).
		On("acme-prod-sa", "*", testkube.Reply{Stderr: "error: You must be logged in to the server (Unauthorized)\n", Exit: 1}).
		On("*", "get pods,*", testkube.Reply{Stdout: core}).
		On("acme-prod-use", "get secrets *", testkube.Reply{Stdout: "Secret   api   api-token   2026-01-02T00:00:00Z\n"}).
		On("*", "get secrets *", testkube.Reply{Stderr: `Error from server (Forbidden): secrets is forbidden` + "\n", Exit: 1}).
		On("acme-prod-use", "get kustomizations*", testkube.Reply{Stdout: "Kustomization   flux-system   api   2026-01-02T00:00:00Z\nHelmRelease   monitoring   grafana   2026-01-02T00:00:00Z\n"}).
		On("*", "get kustomizations*", testkube.Reply{Stderr: `error: the server doesn't have a resource type "kustomizations"` + "\n", Exit: 1})
	r := &fleet.Reader{Client: kube.Client{Kubectl: k.Path, Kubeconfig: k.Kubeconfig, Timeout: 10 * time.Second}}
	clusters := []kube.Context{{Name: "acme-prod-use", Short: "use"}, {Name: "acme-staging", Short: "staging"}, {Name: "acme-prod-sa", Short: "sa"}}
	res := fleet.Read(r, clusters, findQueries(), func(_ kube.Context, outs [][]byte) ([]found, error) {
		return parseFound(outs, "api", false), nil
	})
	if res.OK != 2 || len(res.Skipped) != 1 || !res.Skipped[0].Expired {
		t.Fatalf("ok %d, skipped %v", res.OK, res.Strings())
	}
	sortFound(res.Rows)
	var got []string
	for _, f := range res.Rows {
		got = append(got, f.C.Short+" "+f.V.Kind+" "+nsName(f.V))
	}
	want := "staging Pod api/api-7d9f6c5b8-4kq2x|staging Deployment api/api|staging ConfigMap api/api-config|" +
		"use Pod api/api-7d9f6c5b8-4kq2x|use Deployment api/api|use ConfigMap api/api-config|use Secret api/api-token|use Kustomization flux-system/api"
	if strings.Join(got, "|") != want {
		t.Errorf("found:\n%s\nwant:\n%s", strings.Join(got, "|"), want)
	}
	if res.Rows[0].V.Created.IsZero() || res.Rows[0].V.resource() != "pods" || res.Rows[len(res.Rows)-1].V.resource() != "kustomizations.kustomize.toolkit.fluxcd.io" {
		t.Errorf("row: %+v", res.Rows[0].V)
	}
	items := findItems(res.Rows[:1])
	if !strings.Contains(items[0].Label, "staging  Pod  api/api-7d9f6c5b8-4kq2x") {
		t.Errorf("label %q", items[0].Label)
	}
}
