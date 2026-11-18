package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/vitalbh/rex8s/internal/kube"
)

// kview's CPU and memory columns: metrics-server's API (metrics.k8s.io),
// read with kwatch's client — or `kubectl top` where kwatch has none — in
// the background at most every metricsEvery per cluster and kind, so a tab
// that refreshes every 5s doesn't ask every 5s. Without metrics-server (or
// while the first read runs) the columns show "-"; a cluster without it is
// asked again only after metricsMissing.
const (
	metricsEvery   = 30 * time.Second
	metricsMissing = 5 * time.Minute
)

type kvUsage struct {
	cpu, mem       string // as kubectl top prints them: "250m", "512Mi"
	cpuPct, memPct string // nodes: "12%"
	cpuM, memB     int64  // millicores, bytes: for sorting
}

type kvMetrics struct {
	cl kube.Client
	// api reads the metrics API without kubectl (kview's *kwatch.Hub; nil:
	// kubectl top, as in demo mode).
	api kvMetricsAPI
	// sync: read in the caller's goroutine (tests).
	sync bool

	mu sync.Mutex
	by map[string]*kvMetricsOf // cluster name + "|pods" / "|nodes"
}

type kvMetricsOf struct {
	use     map[string]kvUsage // "ns/pod" or "node"
	tried   time.Time
	missing bool // the last read failed: no metrics-server, or no access
	busy    bool
}

func newKvMetrics(cl kube.Client) *kvMetrics {
	return &kvMetrics{cl: cl, by: map[string]*kvMetricsOf{}}
}

// refresh reads c's metrics of kind ("pods" or "nodes") again when they are
// older than metricsEvery, in the background.
func (m *kvMetrics) refresh(c kube.Context, kind string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	key := c.Name + "|" + kind
	st := m.by[key]
	if st == nil {
		st = &kvMetricsOf{}
		m.by[key] = st
	}
	wait := metricsEvery
	if st.missing {
		wait = metricsMissing
	}
	if st.busy || time.Since(st.tried) < wait {
		m.mu.Unlock()
		return
	}
	first := st.tried.IsZero()
	st.busy, st.tried = true, time.Now()
	m.mu.Unlock()

	// the first read is waited for (a few seconds at most), so the columns
	// are filled from the first rows; later ones run behind
	timeout := 20 * time.Second
	if first {
		timeout = 6 * time.Second
	}
	read := func() {
		use, err := m.read(c, kind, timeout)
		m.mu.Lock()
		defer m.mu.Unlock()
		st.busy, st.missing, st.use = false, err != nil, use // nil on an error: "-", not old numbers
	}
	if m.sync || first {
		read()
	} else {
		go read()
	}
}

// kvMetricsAPI is what kvMetrics needs of kwatch.Hub.
type kvMetricsAPI interface {
	List(ctx context.Context, c kube.Context, gvr schema.GroupVersionResource) (*unstructured.UnstructuredList, bool, error)
	Object(c kube.Context, resource, ns, name string) (map[string]any, bool)
}

func (m *kvMetrics) read(c kube.Context, kind string, timeout time.Duration) (map[string]kvUsage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if m.api != nil {
		if use, ok, err := m.readAPI(ctx, c, kind); ok {
			return use, err
		}
	}
	args := []string{"top", "pods", "-A", "--no-headers"}
	if kind == "nodes" {
		args = []string{"top", "nodes", "--no-headers"}
	}
	out, err := m.cl.Output(ctx, c.Name, args...)
	if err != nil {
		return nil, err
	}
	if kind == "nodes" {
		return parseTopNodes(string(out)), nil
	}
	return parseTopPods(string(out)), nil
}

// parseTopPods reads `kubectl top pods -A --no-headers`: NAMESPACE POD CPU
// MEMORY.
func parseTopPods(out string) map[string]kvUsage {
	use := map[string]kvUsage{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 4 {
			continue
		}
		use[f[0]+"/"+f[1]] = kvUsage{cpu: f[2], mem: f[3], cpuM: parseCPU(f[2]), memB: parseMem(f[3])}
	}
	return use
}

// parseTopNodes reads `kubectl top nodes --no-headers`: NAME CPU CPU% MEMORY
// MEMORY% (a node without metrics says <unknown>).
func parseTopNodes(out string) map[string]kvUsage {
	use := map[string]kvUsage{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 5 || strings.Contains(f[1], "unknown") {
			continue
		}
		use[f[0]] = kvUsage{cpu: f[1], cpuPct: f[2], mem: f[3], memPct: f[4], cpuM: parseCPU(f[1]), memB: parseMem(f[3])}
	}
	return use
}

var (
	podMetricsGVR  = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	nodeMetricsGVR = schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
	nodesGVR       = schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
)

// readAPI reads the metrics API (a list: it can't be watched) into what
// kubectl top would print; ok is false when kwatch has no client for c.
func (m *kvMetrics) readAPI(ctx context.Context, c kube.Context, kind string) (map[string]kvUsage, bool, error) {
	if kind != "nodes" {
		list, ok, err := m.api.List(ctx, c, podMetricsGVR)
		if !ok || err != nil {
			return nil, ok, err
		}
		use := map[string]kvUsage{}
		for _, it := range list.Items {
			// a pod is the sum of its containers, as in kubectl top pods
			var cpu, mem resource.Quantity
			cs, _, _ := unstructured.NestedSlice(it.Object, "containers")
			for _, ct := range cs {
				ctm, _ := ct.(map[string]any)
				u, _ := ctm["usage"].(map[string]any)
				cpu.Add(quantity(u, "cpu"))
				mem.Add(quantity(u, "memory"))
			}
			use[it.GetNamespace()+"/"+it.GetName()] = topUsage(cpu, mem)
		}
		return use, true, nil
	}
	list, ok, err := m.api.List(ctx, c, nodeMetricsGVR)
	if !ok || err != nil {
		return nil, ok, err
	}
	// percentages are of the node's allocatable, as in kubectl top nodes:
	// from the nodes tab's watch, or else one list of the nodes
	alloc := map[string]map[string]any{}
	for _, it := range list.Items {
		if o, ok := m.api.Object(c, "nodes", "", it.GetName()); ok {
			alloc[it.GetName()], _, _ = unstructured.NestedMap(o, "status", "allocatable")
		}
	}
	if len(alloc) < len(list.Items) {
		if nodes, ok, err := m.api.List(ctx, c, nodesGVR); ok && err == nil {
			for _, n := range nodes.Items {
				alloc[n.GetName()], _, _ = unstructured.NestedMap(n.Object, "status", "allocatable")
			}
		}
	}
	use := map[string]kvUsage{}
	for _, it := range list.Items {
		u, _, _ := unstructured.NestedMap(it.Object, "usage")
		cpu, mem := quantity(u, "cpu"), quantity(u, "memory")
		cpuA, memA := quantity(alloc[it.GetName()], "cpu"), quantity(alloc[it.GetName()], "memory")
		v := topUsage(cpu, mem)
		v.cpuPct, v.memPct = topPct(cpu.MilliValue(), cpuA.MilliValue()), topPct(mem.Value(), memA.Value())
		use[it.GetName()] = v
	}
	return use, true, nil
}

// quantity is m[key] ("250m", "1200Ki"); zero when absent or unreadable.
func quantity(m map[string]any, key string) resource.Quantity {
	s, _ := m[key].(string)
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return resource.Quantity{}
	}
	return q
}

// topUsage formats as kubectl top does: millicores, mebibytes.
func topUsage(cpu, mem resource.Quantity) kvUsage {
	return kvUsage{cpu: fmt.Sprintf("%dm", cpu.MilliValue()), mem: fmt.Sprintf("%dMi", mem.Value()/(1<<20)),
		cpuM: cpu.MilliValue(), memB: mem.Value()}
}

// topPct is use of total as kubectl top nodes prints it ("12%"); "-"
// without a total.
func topPct(use, total int64) string {
	if total <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", int64(float64(use)/float64(total)*100))
}

func (m *kvMetrics) get(c kube.Context, kind, key string) (kvUsage, bool) {
	if m == nil {
		return kvUsage{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.by[c.Name+"|"+kind]
	if st == nil || st.use == nil {
		return kvUsage{}, false
	}
	u, ok := st.use[key]
	return u, ok
}

func (m *kvMetrics) pod(c kube.Context, ns, name string) (kvUsage, bool) {
	return m.get(c, "pods", ns+"/"+name)
}

func (m *kvMetrics) node(c kube.Context, name string) (kvUsage, bool) {
	return m.get(c, "nodes", name)
}
