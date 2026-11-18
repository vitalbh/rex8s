package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
)

// kview's CPU and memory columns: `kubectl top` (metrics-server), read in
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

func (m *kvMetrics) read(c kube.Context, kind string, timeout time.Duration) (map[string]kvUsage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
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
