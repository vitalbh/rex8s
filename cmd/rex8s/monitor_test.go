package main

import (
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/testkube"
)

func TestDiffKeys(t *testing.T) {
	added, removed := diffKeys(map[string]int{"a": 1, "b": 1}, map[string]int{"b": 1, "c": 1})
	if len(added) != 1 || added[0] != "c" || len(removed) != 1 || removed[0] != "a" {
		t.Fatalf("added=%v removed=%v", added, removed)
	}
}

// pollAll tells apart a cluster without Flux, an expired login and a read
// that failed; failing items are keyed for the notifications.
func TestPollAll(t *testing.T) {
	k := testkube.New(t).
		On("use", "get kustomizations*", testkube.Reply{Stdout: `{"items":[
		 {"kind":"Kustomization","metadata":{"name":"apps","namespace":"flux-system"},"status":{"conditions":[{"type":"Ready","status":"False","message":"build failed"}]}},
		 {"kind":"Kustomization","metadata":{"name":"infra","namespace":"flux-system"},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`}).
		On("lab", "*", testkube.Reply{Stderr: `error: the server doesn't have a resource type "kustomizations"` + "\n", Exit: 1}).
		On("euw", "*", testkube.Reply{Stderr: "error: You must be logged in to the server (Unauthorized)\n", Exit: 1}).
		On("sa", "*", testkube.Reply{Stderr: "Unable to connect to the server: dial tcp: i/o timeout\n", Exit: 1})
	r := &fleet.Reader{Client: kube.Client{Kubectl: k.Path, Kubeconfig: k.Kubeconfig, Timeout: 10 * time.Second}}
	clusters := []kube.Context{{Name: "use", Short: "use"}, {Name: "lab", Short: "lab"}, {Name: "euw", Short: "euw"}, {Name: "sa", Short: "sa"}}
	h := pollAll(r, clusters)

	if u := h["use"]; !u.installed || u.err != "" || len(u.items) != 2 || len(u.failing) != 1 {
		t.Errorf("use: %+v", u)
	} else if _, ok := u.failing["Kustomization/flux-system/apps"]; !ok {
		t.Errorf("use: failing keys %v", u.failing)
	}
	if l := h["lab"]; l.installed || l.err != "" || l.failing == nil {
		t.Errorf("lab (no Flux): %+v", l)
	}
	if e := h["euw"]; !e.expired || e.err == "" {
		t.Errorf("euw (login expired): %+v", e)
	}
	if s := h["sa"]; s.expired || s.err != "Unable to connect to the server: dial tcp: i/o timeout" {
		t.Errorf("sa: %+v", s)
	}
	if got := tabTitle(clusters, h); got != "Flux 1 failing · use, euw, sa" {
		t.Errorf("tab title %q", got)
	}
}
