package kwatch

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/vitalbh/rex8s/internal/kube"
)

// Reads beside the watches: one object out of what a watch already holds
// (kview's YAML), and a plain list with the Hub's client for what can't be
// watched (metrics.k8s.io).

// Object is resource ns/name ("pods", "deployments", …; ns "" for nodes)
// on cluster c as a watch of this Hub holds it: a copy, with what slim
// drops (managed fields, the last-applied annotation) gone. ok is false
// when no watch on c holds that kind with its first list in and current —
// or holds it but not that object (gone, or filtered out by the watch's
// field selector): the caller asks kubectl.
func (h *Hub) Object(c kube.Context, resource, ns, name string) (obj map[string]any, ok bool) {
	k, served := kinds[resource]
	if !served {
		return nil, false
	}
	key := name
	if k.namespaced {
		key = ns + "/" + name
	}
	prefix := c.Name + "\x00"
	h.mu.Lock()
	var ws []*watcher
	for wk, w := range h.watches {
		if strings.HasPrefix(wk, prefix) {
			ws = append(ws, w)
		}
	}
	h.mu.Unlock()
	for _, w := range ws {
		if !w.synced() || w.failing() != nil {
			continue
		}
		for i, wk := range w.q.kinds {
			if wk.gvr != k.gvr {
				continue
			}
			o, exists, err := w.informers[i].GetStore().GetByKey(key)
			if u, isU := o.(*unstructured.Unstructured); err == nil && exists && isU {
				return u.DeepCopy().Object, true
			}
		}
	}
	return nil, false
}

// List lists gvr across every namespace of cluster c, once, with this
// Hub's client for c (made on first use, like a watch's). ok is false when
// the Hub has no client for c (a legacy auth-provider): the caller runs
// kubectl.
func (h *Hub) List(ctx context.Context, c kube.Context, gvr schema.GroupVersionResource) (list *unstructured.UnstructuredList, ok bool, err error) {
	cl := h.clientOf(c.Name)
	if cl.err != nil {
		return nil, false, nil
	}
	list, err = cl.dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, true, describe(err)
	}
	return list, true, nil
}

// clientOf is ctxName's client, made the first time: the one watch uses.
func (h *Hub) clientOf(ctxName string) *client {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clientLocked(ctxName)
}
