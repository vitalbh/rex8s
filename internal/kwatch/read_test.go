package kwatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/vitalbh/rex8s/internal/kube"
)

// Object answers from a watch's store, slimmed, and only what a current
// watch on that cluster holds.
func TestObject(t *testing.T) {
	h, _ := fakeHub(t, obj("v1", "Pod", "app", "api-1"))
	use := kube.Context{Name: "use"}
	if _, ok := h.Object(use, "pods", "app", "api-1"); ok {
		t.Fatal("answered before any watch")
	}
	if _, ok, err := h.Serve(use, podArgs); !ok || err != nil {
		t.Fatalf("serve: ok=%v err=%v", ok, err)
	}
	o, ok := h.Object(use, "pods", "app", "api-1")
	if !ok {
		t.Fatal("not found in the watch")
	}
	u := unstructured.Unstructured{Object: o}
	if u.GetKind() != "Pod" || u.GetManagedFields() != nil || u.GetAnnotations()["keep"] != "me" ||
		u.GetAnnotations()["kubectl.kubernetes.io/last-applied-configuration"] != "" {
		t.Fatalf("object %v", o)
	}
	u.SetName("changed") // a copy: the store keeps its own
	if again, _ := h.Object(use, "pods", "app", "api-1"); (&unstructured.Unstructured{Object: again}).GetName() != "api-1" {
		t.Error("Object handed out the store's own object")
	}
	for _, c := range []struct{ ctx, res, ns, name string }{
		{"use", "pods", "app", "gone"},         // not there
		{"use", "deployments", "app", "api-1"}, // kind not watched yet
		{"use", "configmaps", "app", "api-1"},  // kind kwatch doesn't serve
		{"other", "pods", "app", "api-1"},      // another cluster
	} {
		if _, ok := h.Object(kube.Context{Name: c.ctx}, c.res, c.ns, c.name); ok {
			t.Errorf("%v answered", c)
		}
	}
}

var podMetricsGVR = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}

// List reads what can't be watched with the Hub's client; a context
// without a client is kubectl's, and API errors read like kubectl's.
func TestList(t *testing.T) {
	m := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "metrics.k8s.io/v1beta1", "kind": "PodMetrics",
		"metadata": map[string]any{"name": "api-1", "namespace": "app"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{podMetricsGVR: "PodMetricsList"})
	if _, err := dyn.Resource(podMetricsGVR).Namespace("app").Create(context.Background(), m, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	h := New("")
	h.newClient = func(ctxName string) (dynamic.Interface, *health, error) {
		if ctxName == "legacy" {
			return nil, nil, errors.New("auth-provider oidc")
		}
		return dyn, nil, nil
	}
	t.Cleanup(h.Stop)
	list, ok, err := h.List(context.Background(), kube.Context{Name: "use"}, podMetricsGVR)
	if !ok || err != nil || len(list.Items) != 1 || list.Items[0].GetName() != "api-1" {
		t.Fatalf("ok=%v err=%v list=%v", ok, err, list)
	}
	if _, ok, _ := h.List(context.Background(), kube.Context{Name: "legacy"}, podMetricsGVR); ok {
		t.Error("a context without a client was served")
	}
	dyn.PrependReactor("list", "pods", func(_ k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(podMetricsGVR.GroupResource(), "")
	})
	if _, ok, err := h.List(context.Background(), kube.Context{Name: "use"}, podMetricsGVR); !ok || err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("no metrics API: ok=%v err=%v", ok, err)
	}
}
