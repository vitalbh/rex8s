package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/testkube"
)

// fakeWatchReads stands in for kwatch.Hub: lists and watched objects per
// cluster; a cluster not in lists has no client (kubectl reads it).
type fakeWatchReads struct {
	lists   map[string]map[schema.GroupVersionResource][]map[string]any // cluster → gvr → items
	errs    map[string]error                                            // cluster → every List fails
	objects map[string]map[string]any                                   // cluster|resource|ns|name
	listed  []string
}

func (f *fakeWatchReads) List(_ context.Context, c kube.Context, gvr schema.GroupVersionResource) (*unstructured.UnstructuredList, bool, error) {
	f.listed = append(f.listed, c.Name+"|"+gvr.Resource)
	if err := f.errs[c.Name]; err != nil {
		return nil, true, err
	}
	by, ok := f.lists[c.Name]
	if !ok {
		return nil, false, nil
	}
	list := &unstructured.UnstructuredList{}
	for _, o := range by[gvr] {
		list.Items = append(list.Items, unstructured.Unstructured{Object: o})
	}
	return list, true, nil
}

func (f *fakeWatchReads) Object(c kube.Context, resource, ns, name string) (map[string]any, bool) {
	o, ok := f.objects[c.Name+"|"+resource+"|"+ns+"|"+name]
	return o, ok
}

func meta(ns, name string) map[string]any {
	m := map[string]any{"name": name}
	if ns != "" {
		m["namespace"] = ns
	}
	return m
}

// With the hub, CPU and memory come from the metrics API, formatted like
// kubectl top (node percentages of allocatable, from the nodes watch or a
// node list); a cluster the hub has no client for runs kubectl top; one
// without the API shows dashes and isn't asked again for metricsMissing.
func TestKviewMetricsAPI(t *testing.T) {
	k, f := kvFake(t)
	gone := kube.Context{Name: "gone", Short: "gone"}
	api := &fakeWatchReads{
		lists: map[string]map[schema.GroupVersionResource][]map[string]any{kvDev.Name: {
			podMetricsGVR: {{"metadata": meta("default", "web-7d9f"), "containers": []any{
				map[string]any{"name": "app", "usage": map[string]any{"cpu": "10500000n", "memory": "30Mi"}},
				map[string]any{"name": "proxy", "usage": map[string]any{"cpu": "2m", "memory": "4096Ki"}}}}},
			nodeMetricsGVR: {
				{"metadata": meta("", "node-a1"), "usage": map[string]any{"cpu": "250m", "memory": "1Gi"}},
				{"metadata": meta("", "node-b1"), "usage": map[string]any{"cpu": "1", "memory": "512Mi"}}},
			nodesGVR: {{"metadata": meta("", "node-b1"), "status": map[string]any{"allocatable": map[string]any{"cpu": "4", "memory": "2Gi"}}}},
		}},
		errs: map[string]error{gone.Name: errors.New("the server could not find the requested resource")},
		objects: map[string]map[string]any{kvDev.Name + "|nodes||node-a1": {"metadata": meta("", "node-a1"),
			"status": map[string]any{"allocatable": map[string]any{"cpu": "2", "memory": "4Gi"}}}},
	}
	m := newKvMetrics(k.cl)
	m.api, m.sync = api, true
	f.On(kvProd.Name, "top pods -A --no-headers", testkube.Reply{Stdout: "default   web-7d9f   7m   8Mi\n"})

	for _, c := range []kube.Context{kvDev, kvProd, gone} {
		m.refresh(c, "pods")
	}
	if u, ok := m.pod(kvDev, "default", "web-7d9f"); !ok || u.cpu != "13m" || u.mem != "34Mi" || u.cpuM != 13 || u.memB != 34<<20 {
		t.Fatalf("dev pod %+v %v", u, ok)
	}
	if u, ok := m.pod(kvProd, "default", "web-7d9f"); !ok || u.cpu != "7m" {
		t.Fatalf("prod pod (kubectl top) %+v %v", u, ok)
	}
	if _, ok := m.pod(gone, "default", "web-7d9f"); ok {
		t.Fatal("a cluster without the metrics API has numbers")
	}
	if n := f.Count("top pods"); n != 1 {
		t.Fatalf("kubectl top ran %d times, want only for prod: %q", n, f.Calls())
	}

	m.refresh(kvDev, "nodes")
	a, _ := m.node(kvDev, "node-a1")
	b, _ := m.node(kvDev, "node-b1")
	if a.cpu != "250m" || a.cpuPct != "12%" || a.mem != "1024Mi" || a.memPct != "25%" {
		t.Errorf("node-a1 (allocatable from the watch) %+v", a)
	}
	if b.cpu != "1000m" || b.cpuPct != "25%" || b.memPct != "25%" {
		t.Errorf("node-b1 (allocatable from a node list) %+v", b)
	}

	before := len(api.listed)
	m.refresh(gone, "pods")
	m.refresh(kvDev, "pods")
	if len(api.listed) != before {
		t.Errorf("asked again within metricsEvery/metricsMissing: %q", api.listed[before:])
	}
}

// y renders what the watch holds, without kubectl; what it doesn't hold,
// kubectl gets.
func TestKviewYAMLFromWatch(t *testing.T) {
	k, f := kvFake(t)
	k.objects = &fakeWatchReads{objects: map[string]map[string]any{
		kvDev.Name + "|pods|default|web": {"apiVersion": "v1", "kind": "Pod", "metadata": meta("default", "web"),
			"status": map[string]any{"phase": "Running"}},
		kvDev.Name + "|deployments|default|api": {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta("default", "api")},
	}}
	f.On(kvProd.Name, "get pods -n default web -o yaml*", testkube.Reply{Stdout: "kind: Pod\n"})

	r := k.podAction(kube.On[kube.Pod]{C: kvDev, V: kube.Pod{Namespace: "default", Name: "web"}}, "y")
	if !strings.HasPrefix(r.Text, "apiVersion: v1\nkind: Pod\nmetadata:\n  name: web\n") || !strings.Contains(r.Text, "phase: Running") ||
		!strings.Contains(r.TextTitle, "watched") {
		t.Fatalf("from the watch: %q %q", r.TextTitle, r.Text)
	}
	r = k.workloadAction(kube.On[kube.Workload]{C: kvDev, V: kube.Workload{Kind: "Deployment", Namespace: "default", Name: "api"}}, "y")
	if !strings.Contains(r.Text, "kind: Deployment") {
		t.Fatalf("deployment: %+v", r)
	}
	if n := len(f.Calls()); n != 0 {
		t.Fatalf("kubectl ran: %q", f.Calls())
	}
	r = k.podAction(kube.On[kube.Pod]{C: kvProd, V: kube.Pod{Namespace: "default", Name: "web"}}, "y")
	if r.Text != "kind: Pod\n" || strings.Contains(r.TextTitle, "watched") || f.Count("get pods -n default web -o yaml") != 1 {
		t.Fatalf("kubectl: %+v %q", r, f.Calls())
	}
	r = k.nodeAction(kube.On[kube.Node]{C: kvDev, V: kube.Node{Name: "node-a1"}}, "y")
	if f.Count("get nodes node-a1 -o yaml") != 1 {
		t.Fatalf("node not watched: kubectl %+v %q", r, f.Calls())
	}
}
