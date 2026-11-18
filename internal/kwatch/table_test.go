package kwatch

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
)

func TestParseTableQuery(t *testing.T) {
	for _, c := range []struct {
		args            string
		ok, all         bool
		resource, group string
	}{
		{"get ingresses.networking.k8s.io -o wide -A", true, true, "ingresses", "networking.k8s.io"},
		{"get configmaps -o wide -A", true, true, "configmaps", ""},
		{"get clusterroles.rbac.authorization.k8s.io -o wide", true, false, "clusterroles", "rbac.authorization.k8s.io"},
		{"get ingresses -A", false, false, "", ""},             // not a wide table
		{"get ingresses -o json -A", false, false, "", ""},     // JSON
		{"get pods,svc -o wide -A", false, false, "", ""},      // two kinds
		{"get ingresses -o wide -n app", false, false, "", ""}, // a flag it doesn't know
	} {
		q, ok := parseTableQuery(strings.Fields(c.args))
		if ok != c.ok || (ok && (q.all != c.all || q.resource != c.resource || q.group != c.group)) {
			t.Errorf("%q: ok=%v %+v", c.args, ok, q)
		}
	}
}

// fakeTables is an API server that prints ingresses as tables: discovery,
// a list, and a watch whose events the test sends.
type fakeTables struct {
	t      *testing.T
	mu     sync.Mutex
	rows   []map[string]any
	events chan string
	lists  int
}

func row(ns, name, rv, class string, created time.Time) map[string]any {
	return map[string]any{
		"cells": []any{name, class, "api.example.com", nil, json.Number("80"), "3d", "a\nb"},
		"object": map[string]any{"kind": "PartialObjectMetadata", "apiVersion": "meta.k8s.io/v1",
			"metadata": map[string]any{"name": name, "namespace": ns, "resourceVersion": rv,
				"creationTimestamp": created.UTC().Format(time.RFC3339)}},
	}
}

var tableColumns = []map[string]any{
	{"name": "Name", "type": "string", "format": "name"},
	{"name": "Class", "type": "string"},
	{"name": "Hosts", "type": "string"},
	{"name": "Address", "type": "string"},
	{"name": "Ports", "type": "string"},
	{"name": "Age", "type": "date"},
	{"name": "Notes", "type": "string", "priority": 1}, // a wide column
}

func (f *fakeTables) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	send := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	switch r.URL.Path {
	case "/apis/networking.k8s.io":
		send(map[string]any{"kind": "APIGroup", "name": "networking.k8s.io",
			"versions":         []any{map[string]any{"groupVersion": "networking.k8s.io/v1", "version": "v1"}},
			"preferredVersion": map[string]any{"groupVersion": "networking.k8s.io/v1", "version": "v1"}})
	case "/apis/networking.k8s.io/v1":
		send(map[string]any{"kind": "APIResourceList", "groupVersion": "networking.k8s.io/v1", "resources": []any{
			map[string]any{"name": "ingresses", "singularName": "ingress", "namespaced": true, "kind": "Ingress",
				"verbs": []string{"get", "list", "watch"}, "shortNames": []string{"ing"}},
			map[string]any{"name": "ingresses/status", "namespaced": true, "kind": "Ingress", "verbs": []string{"get"}},
		}})
	case "/apis/networking.k8s.io/v1/ingresses":
		if !strings.Contains(r.Header.Get("Accept"), "as=Table") || r.URL.Query().Get("includeObject") != "Metadata" {
			f.t.Errorf("not asked for a table: %s %s", r.Header.Get("Accept"), r.URL.RawQuery)
		}
		if r.URL.Query().Get("watch") == "true" {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			for {
				select {
				case ev := <-f.events:
					_, _ = w.Write([]byte(ev + "\n"))
					w.(http.Flusher).Flush()
				case <-r.Context().Done():
					return
				}
			}
		}
		f.mu.Lock()
		f.lists++
		rows := f.rows
		f.mu.Unlock()
		send(map[string]any{"kind": "Table", "apiVersion": "meta.k8s.io/v1", "metadata": map[string]any{"resourceVersion": "10"},
			"columnDefinitions": tableColumns, "rows": rows})
	default:
		w.WriteHeader(http.StatusNotFound)
		send(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "NotFound", "code": 404,
			"message": "the server could not find the requested resource"})
	}
}

// event is a watch event of one row, as the API server sends a table watch.
func event(typ string, r map[string]any) string {
	b, _ := json.Marshal(map[string]any{"type": typ, "object": map[string]any{"kind": "Table", "apiVersion": "meta.k8s.io/v1",
		"metadata": map[string]any{"resourceVersion": r["object"].(map[string]any)["metadata"].(map[string]any)["resourceVersion"]},
		"rows":     []any{r}}})
	return string(b)
}

func tableHub(t *testing.T, rows ...map[string]any) (*Hub, *fakeTables) {
	t.Helper()
	h, _ := fakeHub(t)
	f := &fakeTables{t: t, rows: rows, events: make(chan string, 10)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Cleanup(h.Stop) // before the server closes: its watch ends first
	base, _ := url.Parse(srv.URL)
	h.newTables = func(string) (*tableClient, error) { return &tableClient{base: base, hc: srv.Client()}, nil }
	return h, f
}

var ingArgs = strings.Fields("get ingresses.networking.k8s.io -o wide -A")

func tableOf(t *testing.T, h *Hub) kube.Table {
	t.Helper()
	out, ok, err := h.Serve(kube.Context{Name: "use"}, ingArgs)
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	return kube.ParseTable(string(out))
}

// A table comes out as kubectl prints it (-A: NAMESPACE first; the wide
// columns; AGE from the object), and follows the watch.
func TestServeTable(t *testing.T) {
	created := time.Now().Add(-49 * time.Hour)
	h, f := tableHub(t, row("app", "web", "5", "nginx", created))
	wake := make(chan struct{}, 1)
	use := kube.Context{Name: "use"}
	h.Notify(use, ingArgs, wake)
	tb := tableOf(t, h)
	if got := strings.Join(tb.Columns, "|"); got != "NAMESPACE|NAME|CLASS|HOSTS|ADDRESS|PORTS|AGE|NOTES" {
		t.Fatalf("columns %s", got)
	}
	if len(tb.Rows) != 1 {
		t.Fatalf("rows %v", tb.Rows)
	}
	r := tb.Rows[0]
	if r[0] != "app" || r[1] != "web" || r[2] != "nginx" || r[3] != "api.example.com" || r[4] != "" || r[5] != "80" || r[6] != "2d1h" || r[7] != "a..." {
		t.Errorf("row %q", r)
	}
	if !h.Watching("use", ingArgs) {
		t.Error("not watching")
	}

	drain(wake)
	f.events <- event("ADDED", row("app", "api", "11", "nginx", time.Now()))
	waitFor(t, wake)
	eventually(t, func() bool { return len(tableOf(t, h).Rows) == 2 })
	f.events <- event("MODIFIED", row("app", "web", "12", "traefik", created))
	eventually(t, func() bool {
		for _, r := range tableOf(t, h).Rows {
			if r[1] == "web" && r[2] == "traefik" {
				return true
			}
		}
		return false
	})
	f.events <- event("DELETED", row("app", "api", "13", "nginx", time.Now()))
	eventually(t, func() bool { return len(tableOf(t, h).Rows) == 1 })
}

// A kind the cluster doesn't have, kubectl prints (and says why); so does a
// namespaced kind in one namespace.
func TestServeTableNotServed(t *testing.T) {
	h, _ := tableHub(t)
	use := kube.Context{Name: "use"}
	for _, q := range []string{"get widgets.nope.io -o wide -A", "get ingresses.networking.k8s.io -o wide"} {
		args := strings.Fields(q)
		if _, ok, err := h.Serve(use, args); ok || err != nil {
			t.Errorf("%s: ok=%v err=%v", q, ok, err)
		}
		if h.Watching("use", args) {
			t.Errorf("%s: watching", q)
		}
	}
	if _, ok, _ := h.Serve(kube.Context{Name: "legacy"}, ingArgs); ok || h.Watching("legacy", ingArgs) {
		t.Error("a context without a client was served")
	}
}

// Release stops a tab's watches; a watch nobody served for IdleAfter stops
// too, and the next Serve lists again.
func TestReleaseAndIdle(t *testing.T) {
	h, f := tableHub(t, row("app", "web", "5", "nginx", time.Now()))
	use := kube.Context{Name: "use"}
	wake := make(chan struct{}, 1)
	h.Notify(use, ingArgs, wake)
	tableOf(t, h)
	h.Release(wake)
	if h.Watching("use", ingArgs) {
		t.Fatal("still watching after Release")
	}

	now := time.Now()
	h.now = func() time.Time { return now }
	tableOf(t, h)
	lists := func() int { f.mu.Lock(); defer f.mu.Unlock(); return f.lists }
	before := lists()
	now = now.Add(h.IdleAfter / 2)
	tableOf(t, h) // in use: kept
	if lists() != before {
		t.Fatal("listed again while in use")
	}
	now = now.Add(h.IdleAfter + time.Second)
	h.Serve(use, podArgs) // anything else: the ingresses watch is idle
	if h.Watching("use", ingArgs) {
		t.Fatal("an idle watch kept running")
	}
	if len(tableOf(t, h).Rows) != 1 || lists() == before {
		t.Error("the next Serve didn't list again")
	}
}
