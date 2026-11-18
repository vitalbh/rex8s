package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/kwatch"
	"github.com/vitalbh/rex8s/internal/tui"
)

// ingressAPI is an API server with one Ingress, printed as a table.
func ingressAPI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		send := func(v any) { _ = json.NewEncoder(w).Encode(v) }
		switch r.URL.Path {
		case "/apis/networking.k8s.io":
			send(map[string]any{"kind": "APIGroup", "preferredVersion": map[string]any{"version": "v1"},
				"versions": []any{map[string]any{"version": "v1"}}})
		case "/apis/networking.k8s.io/v1":
			send(map[string]any{"kind": "APIResourceList", "resources": []any{map[string]any{"name": "ingresses",
				"namespaced": true, "kind": "Ingress", "verbs": []string{"list", "watch"}}}})
		case "/apis/networking.k8s.io/v1/ingresses":
			if r.URL.Query().Get("watch") == "true" {
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return
			}
			send(map[string]any{"kind": "Table", "metadata": map[string]any{"resourceVersion": "1"},
				"columnDefinitions": []any{map[string]any{"name": "Name", "type": "string"}, map[string]any{"name": "Hosts", "type": "string"}},
				"rows": []any{map[string]any{"cells": []any{"web", "web.example.com"},
					"object": map[string]any{"metadata": map[string]any{"name": "web", "namespace": "app", "resourceVersion": "1"}}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A ":" tab follows a watch where it can (dev) and kubectl elsewhere (prod,
// behind a legacy auth-provider), and says so; opening another stops its
// watches and closes its wake.
func TestKviewResourceLive(t *testing.T) {
	srv := ingressAPI(t)
	k := fakeKview(t, map[string]string{
		"ctx-production-eu-west-ingresses.networking.k8s.io": "NAMESPACE   NAME   HOSTS\nshop        api    api.example.com\n",
	})
	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	cfg := `apiVersion: v1
kind: Config
clusters:
- name: dev
  cluster: {server: "` + srv.URL + `"}
- name: prod
  cluster: {server: "https://127.0.0.1:1"}
users:
- name: dev
  user: {token: x}
- name: legacy
  user:
    auth-provider: {name: oidc, config: {}}
contexts:
- name: ctx-use
  context: {cluster: dev, user: dev}
- name: ctx-production-eu-west
  context: {cluster: prod, user: legacy}
`
	if err := os.WriteFile(kubeconfig, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	k.hub = kwatch.New(kubeconfig)
	k.hub.FirstWait = 5 * time.Second
	k.fleet.Source = k.hub
	t.Cleanup(k.hub.Stop)

	ing := kube.APIResource{Name: "ingresses", Group: "networking.k8s.io", Kind: "Ingress", Namespaced: true}
	v := k.openResource(ing, nil, "").Open
	items, err := v.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, strings.Join(strings.Fields(tui.Clean(it.Label)), " "))
	}
	if strings.Join(got, "|") != "production-eu-west shop/api api.example.com|use app/web web.example.com" {
		t.Errorf("rows %q", got)
	}
	if v.Wake == nil || v.Live == nil || v.Live() != "live · polling production-eu-west" {
		t.Fatalf("live: %q", v.Live())
	}
	q := []string{"get", "ingresses.networking.k8s.io", "-o", "wide", "-A"}
	if !k.hub.Watching(kvDev.Name, q) {
		t.Fatal("dev isn't watched")
	}

	k.openResource(kube.APIResource{Name: "configmaps", Kind: "ConfigMap", Namespaced: true}, nil, "")
	_, open := <-v.Wake
	if open { // a change it had buffered
		_, open = <-v.Wake
	}
	if open {
		t.Error("the replaced tab's wake is still open")
	}
	if k.hub.Watching(kvDev.Name, q) {
		t.Error("the replaced tab's watch still runs")
	}
}
