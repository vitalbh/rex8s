package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kreq compares what each workload asks for (requests/limits) with what it
// uses now (kubectl top, metrics-server), on this pane's cluster:
//
//   - near the memory limit: OOM risk
//
//   - over-provisioned: uses a small part of what it reserves (cost)
//
//   - no requests: the scheduler can't place it well
//
//     kreq [-n] [FILTER]   all namespaces (-n: this one)
func (a *app) kreq(args []string) error {
	allNS, filter := true, ""
	for _, arg := range args {
		if arg == "-n" {
			allNS = false
		} else {
			filter = arg
		}
	}
	c, err := kube.Current(a.cfg, "", "")
	if err != nil {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	cl.Timeout = 40 * time.Second
	scope, where := []string{"-A"}, c.Short
	if c.AllNS {
		allNS = true
	}
	if !allNS {
		ns := nsOr(c.Namespace)
		scope, where = []string{"-n", ns}, c.Short+"/"+ns
	}
	fmt.Fprintln(os.Stderr, tui.Dim.Render("reading requests and usage on "+where+"…"))
	var podsOut, topOut []byte
	var podsErr, topErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		podsOut, podsErr = cl.Get(c.Name, append([]string{"get", "pods", "-o", "json"}, scope...)...)
	}()
	go func() {
		defer wg.Done()
		topOut, topErr = cl.Get(c.Name, append([]string{"top", "pods", "--no-headers", "--containers"}, scope...)...)
	}()
	wg.Wait()
	fmt.Fprint(os.Stderr, "\033[1A\033[2K")
	if podsErr != nil {
		return podsErr
	}
	if topErr != nil {
		return fmt.Errorf("kubectl top failed (is metrics-server installed?): %v", lastLogLine(topErr.Error()))
	}
	ws, err := workloadResources(podsOut, topOut, allNS, nsOr(c.Namespace))
	if err != nil {
		return err
	}
	if filter != "" {
		f := strings.ToLower(filter)
		kept := ws[:0]
		for _, w := range ws {
			if strings.Contains(strings.ToLower(w.key()), f) {
				kept = append(kept, w)
			}
		}
		ws = kept
	}
	fmt.Print(renderKreq(ws, where))
	return nil
}

// wlRes sums a workload's pods: usage now, requests and limits (cpu in
// millicores, memory in bytes). Pods without metrics are left out.
type wlRes struct {
	ns, name                     string
	pods                         int
	cpuUse, cpuReq, cpuLim       int64
	memUse, memReq, memLim       int64
	noReq                        bool // some container has no requests
	memLimContainers, containers int
	peakMemPct                   float64 // worst container: usage / its memory limit
	peakContainer                string
}

func (w wlRes) key() string { return w.ns + "/" + w.name }

func workloadResources(podsJSON, top []byte, allNS bool, ns string) ([]wlRes, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Name      string `json:"name"`
					Resources struct {
						Requests map[string]string `json:"requests"`
						Limits   map[string]string `json:"limits"`
					} `json:"resources"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(podsJSON, &list); err != nil {
		return nil, err
	}
	// top: [NAMESPACE] POD CONTAINER CPU MEMORY
	type use struct{ cpu, mem int64 }
	usage := map[string]use{} // ns/pod/container
	for _, l := range strings.Split(string(top), "\n") {
		f := strings.Fields(l)
		if !allNS {
			f = append([]string{ns}, f...)
		}
		if len(f) < 5 {
			continue
		}
		usage[f[0]+"/"+f[1]+"/"+f[2]] = use{parseCPU(f[3]), parseMem(f[4])}
	}
	by := map[string]*wlRes{}
	for _, p := range list.Items {
		if p.Status.Phase != "Running" {
			continue
		}
		w := podWorkload(p.Metadata.Name)
		k := p.Metadata.Namespace + "/" + w
		r := by[k]
		if r == nil {
			r = &wlRes{ns: p.Metadata.Namespace, name: w}
			by[k] = r
		}
		measured := false
		for _, ct := range p.Spec.Containers {
			u, ok := usage[p.Metadata.Namespace+"/"+p.Metadata.Name+"/"+ct.Name]
			if !ok {
				continue
			}
			measured = true
			r.containers++
			r.cpuUse += u.cpu
			r.memUse += u.mem
			req, lim := ct.Resources.Requests, ct.Resources.Limits
			if req["cpu"] == "" && req["memory"] == "" {
				r.noReq = true
			}
			r.cpuReq += parseCPU(req["cpu"])
			r.memReq += parseMem(req["memory"])
			r.cpuLim += parseCPU(lim["cpu"])
			if ml := parseMem(lim["memory"]); ml > 0 {
				r.memLim += ml
				r.memLimContainers++
				if pct := float64(u.mem) / float64(ml); pct > r.peakMemPct {
					r.peakMemPct, r.peakContainer = pct, p.Metadata.Name+"/"+ct.Name
				}
			}
		}
		if measured {
			r.pods++
		}
	}
	out := make([]wlRes, 0, len(by))
	for _, r := range by {
		if r.pods > 0 {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key() < out[j].key() })
	return out, nil
}

// parseCPU: "250m" → 250, "2" → 2000, "1500000n" → 1 (millicores).
func parseCPU(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	switch {
	case strings.HasSuffix(s, "m"):
		v, _ := strconv.ParseFloat(strings.TrimSuffix(s, "m"), 64)
		return int64(v)
	case strings.HasSuffix(s, "u"):
		v, _ := strconv.ParseFloat(strings.TrimSuffix(s, "u"), 64)
		return int64(v / 1000)
	case strings.HasSuffix(s, "n"):
		v, _ := strconv.ParseFloat(strings.TrimSuffix(s, "n"), 64)
		return int64(v / 1e6)
	}
	v, _ := strconv.ParseFloat(s, 64)
	return int64(v * 1000)
}

// parseMem: "256Mi", "1Gi", "512M", "1e9", "123456" → bytes.
func parseMem(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	units := []struct {
		suf string
		mul float64
	}{{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40}, {"k", 1e3}, {"K", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suf) {
			v, _ := strconv.ParseFloat(strings.TrimSuffix(s, u.suf), 64)
			return int64(v * u.mul)
		}
	}
	v, _ := strconv.ParseFloat(s, 64)
	return int64(v)
}

func fmtMem(b int64) string {
	switch {
	case b >= 1<<40:
		return fmt.Sprintf("%.1fTi", float64(b)/(1<<40))
	case b >= 1<<30:
		return fmt.Sprintf("%.1fGi", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%dMi", b/(1<<20))
	}
	return fmt.Sprintf("%dKi", b/(1<<10))
}

func pct(a, b int64) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) / float64(b)
}

const (
	oomRisk    = 0.85 // memory usage / limit
	overCPUUse = 0.25 // usage / request below this…
	overMemUse = 0.35
	overMinCPU = 200       // …and reserving at least this (m)
	overMinMem = 512 << 20 // or this
)

func renderKreq(ws []wlRes, where string) string {
	var risk, over, noReq []wlRes
	var wasteCPU, wasteMem int64
	for _, w := range ws {
		if w.peakMemPct >= oomRisk {
			risk = append(risk, w)
		}
		cpuOver := w.cpuReq >= overMinCPU && pct(w.cpuUse, w.cpuReq) < overCPUUse
		memOver := w.memReq >= overMinMem && pct(w.memUse, w.memReq) < overMemUse
		if cpuOver || memOver {
			over = append(over, w)
			if cpuOver {
				wasteCPU += w.cpuReq - w.cpuUse
			}
			if memOver {
				wasteMem += w.memReq - w.memUse
			}
		}
		if w.noReq {
			noReq = append(noReq, w)
		}
	}
	sort.Slice(risk, func(i, j int) bool { return risk[i].peakMemPct > risk[j].peakMemPct })
	sort.Slice(over, func(i, j int) bool {
		return (over[i].cpuReq-over[i].cpuUse)+(over[i].memReq-over[i].memUse)>>22 > (over[j].cpuReq-over[j].cpuUse)+(over[j].memReq-over[j].memUse)>>22
	})
	nw := 0
	for _, w := range ws {
		nw = max(nw, len(w.key()))
	}
	nw = min(nw, 60)

	var b strings.Builder
	head := func(title string, n int, style func(...string) string) {
		fmt.Fprintf(&b, "\n%s %s\n", style(title), tui.Dim.Render(fmt.Sprintf("(%d)", n)))
	}
	head("Near the memory limit — OOM risk", len(risk), tui.Err.Render)
	if len(risk) == 0 {
		b.WriteString(tui.Dim.Render("  none above 85% of its limit") + "\n")
	}
	for _, w := range risk[:min(len(risk), 15)] {
		fmt.Fprintf(&b, "  %-*s  %s of the limit  %s\n", nw, clip(w.key(), nw),
			tui.Err.Render(fmt.Sprintf("%3.0f%%", w.peakMemPct*100)), tui.Dim.Render(w.peakContainer))
	}
	head("Over-provisioned — reserves much more than it uses", len(over), tui.Warn.Render)
	if len(over) == 0 {
		b.WriteString(tui.Dim.Render("  none") + "\n")
	}
	for _, w := range over[:min(len(over), 20)] {
		fmt.Fprintf(&b, "  %-*s  cpu %5dm of %5dm (%3.0f%%)   mem %7s of %7s (%3.0f%%)   %s\n", nw, clip(w.key(), nw),
			w.cpuUse, w.cpuReq, pct(w.cpuUse, w.cpuReq)*100, fmtMem(w.memUse), fmtMem(w.memReq), pct(w.memUse, w.memReq)*100,
			tui.Dim.Render(fmt.Sprintf("%d pod(s)", w.pods)))
	}
	if len(over) > 20 {
		fmt.Fprintf(&b, "  %s\n", tui.Dim.Render(fmt.Sprintf("… %d more", len(over)-20)))
	}
	head("No requests — the scheduler can't plan for it", len(noReq), tui.Bold.Render)
	if len(noReq) == 0 {
		b.WriteString(tui.Dim.Render("  none") + "\n")
	}
	var names []string
	for _, w := range noReq {
		names = append(names, w.key())
	}
	if len(names) > 0 {
		b.WriteString("  " + listMore(names, 8) + "\n")
	}
	fmt.Fprintf(&b, "\n%s\n%s\n", tui.Dim.Render(fmt.Sprintf("%s · %d workloads measured · usage is a snapshot (kubectl top)", where, len(ws))),
		tui.Dim.Render(fmt.Sprintf("reserved but unused in the over-provisioned ones: %dm cpu, %s memory", wasteCPU, fmtMem(wasteMem))))
	return b.String()
}
