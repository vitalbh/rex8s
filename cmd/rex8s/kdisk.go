package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kdisk: how full the volumes are — every PVC's real usage (from each
// node's kubelet, through the API server proxy) and the nodes' own disks,
// fullest first.
//
//	kdisk [-A] [-a] [FILTER]   this pane's cluster (-A: every cluster);
//	                           -a: every volume, not only the ones above 70%
func (a *app) kdisk(args []string) error {
	every, all, filter := false, false, ""
	for _, arg := range args {
		switch arg {
		case "-A":
			every = true
		case "-a":
			all = true
		default:
			filter = arg
		}
	}
	var clusters []kube.Context
	if every {
		raw, err := kube.Load(a.src)
		if err != nil {
			return err
		}
		clusters = uniqueClusters(kube.Contexts(raw, a.cfg))
	} else {
		c, err := kube.Current(a.cfg, "", "")
		if err != nil {
			return err
		}
		clusters = []kube.Context{c}
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	cl.Timeout = 30 * time.Second
	fmt.Fprintln(os.Stderr, tui.Dim.Render(fmt.Sprintf("reading volume usage from the nodes of %d cluster(s)…", len(clusters))))

	var mu sync.Mutex
	var vols []diskUse
	var skipped []string
	var wg sync.WaitGroup
	for _, c := range clusters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vs, err := clusterDisks(cl, c)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				why := lastLogLine(err.Error())
				if kube.AuthExpired(err) {
					why = "login expired (kauth " + c.Short + ")"
				}
				skipped = append(skipped, c.Short+": skipped, "+why)
				return
			}
			vols = append(vols, vs...)
		}()
	}
	wg.Wait()
	fmt.Fprint(os.Stderr, "\033[1A\033[2K")
	if filter != "" {
		f := strings.ToLower(filter)
		kept := vols[:0]
		for _, v := range vols {
			if strings.Contains(strings.ToLower(v.name+" "+v.pod), f) {
				kept = append(kept, v)
			}
		}
		vols = kept
	}
	fmt.Print(renderDisks(vols, all, len(clusters) > 1))
	sort.Strings(skipped)
	for _, s := range skipped {
		fmt.Fprintln(os.Stderr, tui.Warn.Render("  "+s))
	}
	return nil
}

type diskUse struct {
	cluster    kube.Context
	kind       string // pvc, node
	name       string // ns/pvc, or the node
	pod        string // the pod using the PVC
	node       string // where it was measured
	used, size int64
	shared     []string // node: PVCs that live on its disk (local-path, hostPath)
}

func (d diskUse) pct() float64 { return pct(d.used, d.size) }

func clusterDisks(cl kube.Client, c kube.Context) ([]diskUse, error) {
	out, err := cl.Get(c.Name, "get", "nodes", "-o", "json")
	if err != nil {
		return nil, err
	}
	nodes, err := parseNodes(out)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var res []diskUse
	seen := map[string]bool{} // a PVC mounted by several pods counts once
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, n := range nodes {
		if !n.Ready {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := cl.Get(c.Name, "get", "--raw", "/api/v1/nodes/"+n.Name+"/proxy/stats/summary")
			if err != nil {
				return
			}
			ds := parseStatsSummary(data, n.Name)
			mu.Lock()
			defer mu.Unlock()
			for _, d := range ds {
				if seen[d.kind+d.name] {
					continue
				}
				seen[d.kind+d.name] = true
				d.cluster = c
				res = append(res, d)
			}
		}()
	}
	wg.Wait()
	return res, nil
}

// parseStatsSummary reads the kubelet's /stats/summary: the node's root
// filesystem and every PVC its pods mount.
func parseStatsSummary(data []byte, node string) []diskUse {
	type fs struct {
		UsedBytes     *int64 `json:"usedBytes"`
		CapacityBytes *int64 `json:"capacityBytes"`
	}
	var sum struct {
		Node struct {
			Fs fs `json:"fs"`
		} `json:"node"`
		Pods []struct {
			PodRef struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"podRef"`
			Volume []struct {
				fs
				PvcRef *struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"pvcRef"`
			} `json:"volume"`
		} `json:"pods"`
	}
	if json.Unmarshal(data, &sum) != nil {
		return nil
	}
	var out []diskUse
	if f := sum.Node.Fs; f.UsedBytes != nil && f.CapacityBytes != nil && *f.CapacityBytes > 0 {
		out = append(out, diskUse{kind: "node", name: node, node: node, used: *f.UsedBytes, size: *f.CapacityBytes})
	}
	for _, p := range sum.Pods {
		for _, v := range p.Volume {
			if v.PvcRef == nil || v.UsedBytes == nil || v.CapacityBytes == nil || *v.CapacityBytes == 0 {
				continue
			}
			out = append(out, diskUse{kind: "pvc", name: v.PvcRef.Namespace + "/" + v.PvcRef.Name, pod: p.PodRef.Name,
				node: node, used: *v.UsedBytes, size: *v.CapacityBytes})
		}
	}
	return foldShared(out)
}

// foldShared: a PVC that reports its node's own disk (same capacity, about
// the same usage) has no size of its own — local-path or hostPath. It is
// listed under the node instead of repeating the node's numbers.
func foldShared(ds []diskUse) []diskUse {
	nodeIdx := map[string]int{}
	for i, d := range ds {
		if d.kind == "node" {
			nodeIdx[d.node] = i
		}
	}
	var out []diskUse
	var shared = map[string][]string{}
	for _, d := range ds {
		if d.kind == "pvc" {
			if i, ok := nodeIdx[d.node]; ok {
				n := ds[i]
				diff := d.used - n.used
				if diff < 0 {
					diff = -diff
				}
				if d.size == n.size && float64(diff) <= 0.02*float64(n.size) {
					shared[d.node] = append(shared[d.node], d.name)
					continue
				}
			}
		}
		out = append(out, d)
	}
	for i := range out {
		if out[i].kind == "node" {
			out[i].shared = shared[out[i].node]
			sort.Strings(out[i].shared)
		}
	}
	return out
}

const (
	diskWarn = 0.70
	diskBad  = 0.85
)

func renderDisks(ds []diskUse, all, showCluster bool) string {
	sort.Slice(ds, func(i, j int) bool { return ds[i].pct() > ds[j].pct() })
	var shown []diskUse
	pvcs, nodes := 0, 0
	for _, d := range ds {
		if d.kind == "pvc" {
			pvcs++
		} else {
			nodes++
			pvcs += len(d.shared)
		}
		if all || d.pct() >= diskWarn {
			shown = append(shown, d)
		}
	}
	var b strings.Builder
	if len(shown) == 0 {
		b.WriteString(tui.Ok.Render(fmt.Sprintf("every volume below %.0f%%", diskWarn*100)) + "\n")
	}
	cw, nw := 0, 0
	for _, d := range shown {
		cw = max(cw, len(d.cluster.Short))
		nw = max(nw, len(d.name))
	}
	nw = min(nw, 56)
	for _, d := range shown {
		p := d.pct()
		bar := diskBar(p, 20)
		pc := fmt.Sprintf("%3.0f%%", p*100)
		switch {
		case p >= diskBad:
			bar, pc = tui.Err.Render(bar), tui.Err.Render(pc)
		case p >= diskWarn:
			bar, pc = tui.Warn.Render(bar), tui.Warn.Render(pc)
		default:
			bar = tui.Dim.Render(bar)
		}
		cluster := ""
		if showCluster {
			cluster = fmt.Sprintf("%-*s  ", cw, d.cluster.Short)
			if d.cluster.Prod {
				cluster = tui.Err.Render(cluster)
			}
		}
		who := ""
		if d.pod != "" {
			who = tui.Dim.Render(" " + d.pod)
		}
		if len(d.shared) > 0 {
			who = tui.Dim.Render(fmt.Sprintf(" holds %d PVC(s): %s", len(d.shared), listMore(d.shared, 3)))
		}
		fmt.Fprintf(&b, "%s%-4s %-*s  %s %s  %8s of %-8s%s\n", cluster, d.kind, nw, clip(d.name, nw), bar, pc,
			fmtMem(d.used), fmtMem(d.size), who)
	}
	hidden := ""
	if !all {
		hidden = fmt.Sprintf(" · showing those above %.0f%% (-a: all)", diskWarn*100)
	}
	fmt.Fprintf(&b, "\n%s\n", tui.Dim.Render(fmt.Sprintf("%d PVC(s) and %d node disk(s) measured%s", pvcs, nodes, hidden)))
	return b.String()
}

func diskBar(p float64, w int) string {
	n := min(int(p*float64(w)+0.5), w)
	return strings.Repeat("█", n) + strings.Repeat("░", w-n)
}
