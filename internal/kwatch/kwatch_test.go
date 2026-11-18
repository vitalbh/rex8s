package kwatch

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/vitalbh/rex8s/internal/kube"
)

func TestParseQuery(t *testing.T) {
	for _, c := range []struct {
		args   string
		ok     bool
		kinds  int
		fields string
	}{
		{"get pods -A -o json", true, 1, ""},
		{"get deployments,statefulsets,daemonsets -A -o json", true, 3, ""},
		{"get events -A --field-selector type=Warning -o json", true, 1, "type=Warning"},
		{"get nodes -o json", true, 1, ""},                // cluster-scoped: no -A needed
		{"get pods -o json", false, 0, ""},                // one namespace: not served
		{"get pods -A -o wide", false, 0, ""},             // a table
		{"get configmaps -A -o json", false, 0, ""},       // a kind it doesn't watch
		{"get pods -A -o json -l app=x", false, 0, ""},    // a flag it doesn't know
		{"top pods -A", false, 0, ""},                     //
		{"get jobs --all-namespaces -ojson", true, 1, ""}, //
	} {
		q, ok := parseQuery(strings.Fields(c.args))
		if ok != c.ok || (ok && (len(q.kinds) != c.kinds || q.fields != c.fields)) {
			t.Errorf("%q: ok=%v kinds=%d fields=%q", c.args, ok, len(q.kinds), q.fields)
		}
	}
}

var (
	podsGVR   = schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	deployGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

func obj(apiVersion, kind, ns, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": ns,
			"managedFields": []any{map[string]any{"manager": "kubectl"}},
			"annotations":   map[string]any{"kubectl.kubernetes.io/last-applied-configuration": "{…}", "keep": "me"}},
		"status": map[string]any{"phase": "Running"},
	}}
	return u
}

// fakeHub serves from a fake API server holding objs.
func fakeHub(t *testing.T, objs ...runtime.Object) (*Hub, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		podsGVR: "PodList", deployGVR: "DeploymentList",
		{Group: "apps", Version: "v1", Resource: "statefulsets"}: "StatefulSetList",
		{Group: "apps", Version: "v1", Resource: "daemonsets"}:   "DaemonSetList",
	}, objs...)
	h := New("")
	h.FirstWait = 5 * time.Second
	h.newClient = func(ctxName string) (dynamic.Interface, *health, error) {
		if ctxName == "legacy" {
			return nil, nil, errors.New("auth-provider oidc")
		}
		return dyn, nil, nil
	}
	h.newTables = func(string) (*tableClient, error) { return nil, errors.New("no tables here") }
	t.Cleanup(h.Stop)
	return h, dyn
}

var podArgs = strings.Fields("get pods -A -o json")

func names(t *testing.T, out []byte) []string {
	t.Helper()
	ps, err := kube.ParsePods(out)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range ps {
		got = append(got, p.Namespace+"/"+p.Name)
	}
	return got
}

// The first Serve lists; then changes come in, wake the subscriber, and
// the next Serve has them — in kubectl's JSON, slimmed.
func TestServeFollowsChanges(t *testing.T) {
	h, dyn := fakeHub(t, obj("v1", "Pod", "app", "api-1"))
	use := kube.Context{Name: "use"}
	wake := make(chan struct{}, 1)
	h.Notify(use, podArgs, wake)
	out, ok, err := h.Serve(use, podArgs)
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got := names(t, out); len(got) != 1 || got[0] != "app/api-1" {
		t.Fatalf("first list: %v", got)
	}
	s := string(out)
	if strings.Contains(s, "managedFields") || strings.Contains(s, "last-applied") || !strings.Contains(s, `"keep":"me"`) || !strings.Contains(s, `"kind":"Pod"`) {
		t.Errorf("not slimmed or kind missing: %s", s)
	}

	drain(wake)
	pods := dyn.Resource(podsGVR).Namespace("app")
	if _, err := pods.Create(context.Background(), obj("v1", "Pod", "app", "api-2"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wake)
	eventually(t, func() bool { out, _, _ := h.Serve(use, podArgs); return len(names(t, out)) == 2 })

	drain(wake)
	if err := pods.Delete(context.Background(), "api-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, wake)
	eventually(t, func() bool {
		out, _, _ := h.Serve(use, podArgs)
		got := names(t, out)
		return len(got) == 1 && got[0] == "app/api-2"
	})
}

// Several kinds in one query come out together, each with its kind.
func TestServeManyKinds(t *testing.T) {
	h, _ := fakeHub(t, obj("apps/v1", "Deployment", "app", "api"))
	out, ok, err := h.Serve(kube.Context{Name: "use"}, strings.Fields("get deployments,statefulsets,daemonsets -A -o json"))
	if !ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	ws, err := kube.ParseWorkloads(out)
	if err != nil || len(ws) != 1 || ws[0].Name != "api" {
		t.Fatalf("workloads %+v err %v", ws, err)
	}
}

// What it can't serve, kubectl reads: another query, a context without a
// client.
func TestServeLeavesTheRestToKubectl(t *testing.T) {
	h, _ := fakeHub(t)
	if _, ok, _ := h.Serve(kube.Context{Name: "use"}, strings.Fields("get configmaps -A -o json")); ok {
		t.Error("a configmaps query was served")
	}
	if _, ok, _ := h.Serve(kube.Context{Name: "legacy"}, podArgs); ok {
		t.Error("a context without a client was served")
	}
}

// A watch error before the first list answers at once, not after FirstWait;
// after it, a list that never came is an error without waiting.
func TestServeFailsFast(t *testing.T) {
	h, dyn := fakeHub(t)
	dyn.PrependReactor("list", "pods", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewUnauthorized("token expired")
	})
	dyn.PrependWatchReactor("pods", func(_ k8stesting.Action) (bool, watch.Interface, error) {
		return true, nil, apierrors.NewUnauthorized("token expired") // a watch list (the default) asks this first
	})
	start := time.Now()
	_, ok, err := h.Serve(kube.Context{Name: "use"}, podArgs)
	if !ok || err == nil || err.Error() != "Unauthorized" || !kube.AuthExpired(err) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %s: it waited for FirstWait", d)
	}
}

func TestDescribe(t *testing.T) {
	dial := &url.Error{Op: "Get", URL: "https://10.1.2.3:6443/api/v1/pods?limit=500",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("i/o timeout")}}
	for _, c := range []struct {
		err  error
		want string
	}{
		{dial, "unreachable (10.1.2.3:6443)"},
		{apierrors.NewUnauthorized("x"), "Unauthorized"},
		{apierrors.NewForbidden(podsGVR.GroupResource(), "", errors.New("no")), `pods is forbidden: no`},
		{errors.New("other"), "other"},
	} {
		if got := describe(c.err).Error(); got != c.want {
			t.Errorf("describe(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

func drain(ch chan struct{}) {
	select {
	case <-ch:
	default:
	}
}

func waitFor(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no change came")
	}
}

func eventually(t *testing.T, f func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatal("never happened")
}

// Connections that fail say the cluster is unreachable: at once before the
// first list, after failingFor once rows are shown; one that works clears it.
func TestHealth(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	up := ln.Addr().String()
	ln.Close() // nothing listens there now
	hl := &health{}
	dial := hl.dial(&net.Dialer{Timeout: time.Second})
	if _, err := dial(context.Background(), "tcp", up); err == nil {
		t.Fatal("dialed a closed port")
	}
	if err := hl.failing(0); err == nil || err.Error() != "unreachable ("+up+")" {
		t.Fatalf("failing(0) = %v", err)
	}
	if err := hl.failing(time.Hour); err != nil {
		t.Fatalf("failing(1h) = %v", err)
	}
	ln, err = net.Listen("tcp", up)
	if err != nil {
		t.Skip("port taken meanwhile")
	}
	defer ln.Close()
	c, err := dial(context.Background(), "tcp", up)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	if err := hl.failing(0); err != nil {
		t.Fatalf("after a good dial: %v", err)
	}
}
