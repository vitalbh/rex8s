package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// section is one line group of the kstatus report.
type section struct {
	name  string
	bad   bool // something needs attention (red label)
	skip  bool // not applicable on this cluster (not shown)
	lines []string
	hint  string // command to dig in, e.g. "kfail"
}

// kstatus: one screen with the cluster's health — nodes, pods, workloads,
// Flux, jobs, firing alerts (Alertmanager or Prometheus inside the cluster,
// when there is one), certificates (cert-manager) and recent warnings.
// -w refreshes every 30s.
func (a *app) kstatus(args []string) error {
	watch := false
	for _, arg := range args {
		if arg == "-w" {
			watch = true
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
	if watch {
		rex.Rename("status " + c.Short)
	}
	for {
		start := time.Now()
		out := renderStatus(c, gatherStatus(cl, c.Name), start)
		if watch {
			fmt.Print("\x1b[H\x1b[2J")
		}
		fmt.Print(out)
		if !watch {
			return nil
		}
		fmt.Print(tui.Dim.Render("\nrefreshes every 30s · ctrl+c to quit"))
		time.Sleep(30 * time.Second)
	}
}

func gatherStatus(cl kube.Client, ctxName string) []section {
	fns := []func(kube.Client, string) section{
		statusNodes, statusPods, statusWorkloads, statusFlux, statusJobs, statusAlerts, statusCerts, statusWarnings,
	}
	out := make([]section, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = fn(cl, ctxName)
		}()
	}
	wg.Wait()
	return out
}

func renderStatus(c kube.Context, secs []section, at time.Time) string {
	var b strings.Builder
	head := tui.Bold.Render(c.Short)
	if c.Prod {
		head += " " + tui.Err.Render("PROD")
	}
	b.WriteString(head + tui.Dim.Render(" · "+c.Name+" · "+at.Format("15:04:05")) + "\n\n")
	for _, s := range secs {
		if s.skip {
			continue
		}
		label := tui.Ok.Render(fmt.Sprintf("%-10s", s.name))
		if s.bad {
			label = tui.Err.Render(fmt.Sprintf("%-10s", s.name))
		}
		for i, l := range s.lines {
			if i == 0 {
				b.WriteString(label + " " + l)
				if s.hint != "" {
					b.WriteString(tui.Dim.Render("  → " + s.hint))
				}
			} else {
				b.WriteString(strings.Repeat(" ", 11) + l)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

func errSection(name string, err error) section {
	return section{name: name, bad: true, lines: []string{tui.Dim.Render(truncate(lastLogLine(err.Error()), 120))}}
}

// ── nodes ──────────────────────────────────────────────────────────────

func statusNodes(cl kube.Client, ctxName string) section {
	out, err := cl.Get(ctxName, "get", "nodes", "-o", "json")
	if err != nil {
		return errSection("Nodes", err)
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Unschedulable bool `json:"unschedulable"`
			} `json:"spec"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
				NodeInfo struct {
					KubeletVersion string `json:"kubeletVersion"`
				} `json:"nodeInfo"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return errSection("Nodes", err)
	}
	s := section{name: "Nodes"}
	ready, versions := 0, map[string]bool{}
	var problems []string
	for _, n := range list.Items {
		versions[n.Status.NodeInfo.KubeletVersion] = true
		for _, c := range n.Status.Conditions {
			switch {
			case c.Type == "Ready" && c.Status == "True":
				ready++
			case c.Type == "Ready":
				problems = append(problems, n.Metadata.Name+" NotReady")
			case c.Status == "True" && strings.HasSuffix(c.Type, "Pressure"):
				problems = append(problems, n.Metadata.Name+" "+c.Type)
			}
		}
		if n.Spec.Unschedulable {
			problems = append(problems, n.Metadata.Name+" cordoned")
		}
	}
	var vs []string
	for v := range versions {
		vs = append(vs, v)
	}
	sort.Strings(vs)
	first := fmt.Sprintf("%d/%d ready · %s", ready, len(list.Items), strings.Join(vs, ", "))
	if len(problems) > 0 {
		s.bad = true
		first += " · " + tui.Warn.Render(strings.Join(problems, ", "))
	}
	s.lines = append(s.lines, first)
	if usage := nodeUsage(cl, ctxName); usage != "" {
		s.lines = append(s.lines, usage)
	}
	return s
}

// nodeUsage summarizes `kubectl top nodes` (needs metrics-server; "" without it).
func nodeUsage(cl kube.Client, ctxName string) string {
	out, err := cl.Get(ctxName, "top", "nodes", "--no-headers")
	if err != nil {
		return ""
	}
	type nu struct {
		name     string
		cpu, mem int
	}
	var ns []nu
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		cpu, err1 := strconv.Atoi(strings.TrimSuffix(f[2], "%"))
		mem, err2 := strconv.Atoi(strings.TrimSuffix(f[4], "%"))
		if err1 == nil && err2 == nil {
			ns = append(ns, nu{f[0], cpu, mem})
		}
	}
	if len(ns) == 0 {
		return ""
	}
	sumC, sumM, hot := 0, 0, ns[0]
	for _, n := range ns {
		sumC += n.cpu
		sumM += n.mem
		if max(n.cpu, n.mem) > max(hot.cpu, hot.mem) {
			hot = n
		}
	}
	line := fmt.Sprintf("cpu %d%% · mem %d%% (average)", sumC/len(ns), sumM/len(ns))
	if top := max(hot.cpu, hot.mem); top >= 85 {
		line += " · " + tui.Warn.Render(fmt.Sprintf("%s cpu %d%% mem %d%%", hot.name, hot.cpu, hot.mem))
	} else {
		line += tui.Dim.Render(fmt.Sprintf(" · busiest %s cpu %d%% mem %d%%", hot.name, hot.cpu, hot.mem))
	}
	return line
}

// ── pods ───────────────────────────────────────────────────────────────

func statusPods(cl kube.Client, ctxName string) section {
	out, err := cl.Get(ctxName, "get", "pods", "-A", "-o", "json")
	if err != nil {
		return errSection("Pods", err)
	}
	ps, err := parsePods(out)
	if err != nil {
		return errSection("Pods", err)
	}
	count := map[string]int{}
	for _, p := range ps {
		count[p.badge()]++
	}
	s := section{name: "Pods"}
	first := fmt.Sprintf("%d running", count["ok"])
	if count["FAIL"] > 0 {
		s.bad, s.hint = true, "kfail"
		first += " · " + tui.Err.Render(fmt.Sprintf("%d failing", count["FAIL"]))
	}
	if count["WAIT"] > 0 {
		first += " · " + tui.Warn.Render(fmt.Sprintf("%d not ready", count["WAIT"]))
		s.hint = "kfail"
	}
	if count["done"] > 0 {
		first += tui.Dim.Render(fmt.Sprintf(" · %d completed", count["done"]))
	}
	s.lines = append(s.lines, first)

	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Restarts > ps[j].Restarts })
	var top []string
	for _, p := range ps[:min(len(ps), 3)] {
		if p.Restarts >= 5 {
			top = append(top, fmt.Sprintf("%s/%s %s", p.Namespace, p.Name, tui.Warn.Render(fmt.Sprintf("×%d", p.Restarts))))
		}
	}
	if len(top) > 0 {
		s.lines = append(s.lines, tui.Dim.Render("most restarts: ")+strings.Join(top, " · "))
	}
	return s
}

// ── workloads ──────────────────────────────────────────────────────────

func statusWorkloads(cl kube.Client, ctxName string) section {
	out, err := cl.Get(ctxName, "get", "deployments,statefulsets,daemonsets", "-A", "-o", "json")
	if err != nil {
		return errSection("Workloads", err)
	}
	ws, err := kube.ParseWorkloads(out)
	if err != nil {
		return errSection("Workloads", err)
	}
	s := section{name: "Workloads"}
	var bad, rolling []string
	for _, w := range ws {
		switch {
		case w.RolledOut():
		case w.InProgress() && !w.Failed:
			rolling = append(rolling, fmt.Sprintf("%s %s/%s", kube.ShortKind(w.Kind), w.Namespace, w.Name))
		default:
			state := fmt.Sprintf("%d/%d", w.Ready, w.Desired)
			if w.Failed {
				state = "stuck" // ProgressDeadlineExceeded
			}
			bad = append(bad, fmt.Sprintf("%s %s/%s %s", kube.ShortKind(w.Kind), w.Namespace, w.Name, state))
		}
	}
	first := fmt.Sprintf("%d total", len(ws))
	if len(bad) > 0 {
		s.bad = true
		first += " · " + tui.Err.Render(fmt.Sprintf("%d unavailable", len(bad)))
	}
	if len(rolling) > 0 {
		first += " · " + tui.Warn.Render(fmt.Sprintf("%d rolling out", len(rolling)))
	}
	s.lines = append(s.lines, first)
	if len(bad) > 0 {
		s.lines = append(s.lines, listMore(bad, 4))
	}
	return s
}

// ── flux ───────────────────────────────────────────────────────────────

func statusFlux(cl kube.Client, ctxName string) section {
	items, installed, err := cl.FluxItems(ctxName)
	if err != nil {
		return errSection("Flux", err)
	}
	if !installed {
		return section{skip: true}
	}
	st := kube.Summarize(items)
	s := section{name: "Flux"}
	first := fmt.Sprintf("%d ready", st.Ready)
	if st.Failing > 0 {
		s.bad, s.hint = true, "kflux"
		first += " · " + tui.Err.Render(fmt.Sprintf("%d failing", st.Failing))
	}
	if st.Progressing > 0 {
		first += " · " + tui.Warn.Render(fmt.Sprintf("%d reconciling", st.Progressing))
	}
	if st.Suspended > 0 {
		first += tui.Dim.Render(fmt.Sprintf(" · %d suspended", st.Suspended))
	}
	s.lines = append(s.lines, first)
	n := 0
	for _, p := range st.Problems {
		if p.Suspended || n == 3 {
			continue
		}
		n++
		s.lines = append(s.lines, fmt.Sprintf("%s %s/%s %s", p.Kind, p.Namespace, p.Name, tui.Dim.Render(truncate(oneLine(p.Message), 90))))
	}
	return s
}

// ── jobs ───────────────────────────────────────────────────────────────

func statusJobs(cl kube.Client, ctxName string) section {
	jout, err := cl.Get(ctxName, "get", "jobs", "-A", "-o", "json")
	if err != nil {
		return errSection("Jobs", err)
	}
	jobs, err := parseJobs(jout)
	if err != nil {
		return errSection("Jobs", err)
	}
	var crons []cronJob
	if cout, err := cl.Get(ctxName, "get", "cronjobs", "-A", "-o", "json"); err == nil {
		crons, _ = parseCronJobs(cout)
	}
	if len(jobs) == 0 && len(crons) == 0 {
		return section{skip: true}
	}
	s := section{name: "Jobs"}
	failed, running := 0, 0
	for _, j := range jobs {
		switch {
		case j.State == "failed" && time.Since(j.Start) < 24*time.Hour:
			failed++
		case j.State == "running":
			running++
		}
	}
	var failingCrons, suspended []string
	for _, c := range crons {
		switch cronState(c, jobs) {
		case "failed":
			failingCrons = append(failingCrons, c.Namespace+"/"+c.Name)
		case "suspended":
			suspended = append(suspended, c.Namespace+"/"+c.Name)
		}
	}
	first := fmt.Sprintf("%s · %d running", plural(len(crons), "cronjob"), running)
	if failed > 0 {
		s.bad, s.hint = true, "kjobs failed"
		first += " · " + tui.Err.Render(fmt.Sprintf("%d failed in 24h", failed))
	}
	if len(suspended) > 0 {
		first += tui.Dim.Render(fmt.Sprintf(" · %d suspended", len(suspended)))
	}
	s.lines = append(s.lines, first)
	if len(failingCrons) > 0 {
		s.bad, s.hint = true, "kjobs failed"
		s.lines = append(s.lines, tui.Dim.Render("last run failed: ")+listMore(failingCrons, 4))
	}
	return s
}

// ── alerts ─────────────────────────────────────────────────────────────

type firingAlert struct {
	Name, Severity, Summary string
}

// statusAlerts reads firing alerts from an Alertmanager or Prometheus that
// runs in the cluster, through the API server's service proxy (no
// port-forward). Not shown when the cluster has neither.
func statusAlerts(cl kube.Client, ctxName string) section {
	out, err := cl.Get(ctxName, "get", "services", "-A", "-o", "json")
	if err != nil {
		return section{skip: true}
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Ports []struct {
					Port int `json:"port"`
				} `json:"ports"`
			} `json:"spec"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &list) != nil {
		return section{skip: true}
	}
	type source struct{ ns, name, port, kind string }
	var sources []source
	for _, svc := range list.Items {
		name := svc.Metadata.Name
		kind := ""
		switch {
		case strings.Contains(name, "alertmanager"):
			kind = "alertmanager"
		case strings.Contains(name, "prometheus") && !strings.Contains(name, "operator") && !strings.Contains(name, "exporter"):
			kind = "prometheus"
		default:
			continue
		}
		for _, p := range svc.Spec.Ports {
			if (kind == "alertmanager" && p.Port == 9093) || (kind == "prometheus" && p.Port == 9090) {
				sources = append(sources, source{svc.Metadata.Namespace, name, strconv.Itoa(p.Port), kind})
			}
		}
	}
	if len(sources) == 0 {
		return section{skip: true}
	}
	// Alertmanager first: it has every firing alert, deduplicated
	sort.SliceStable(sources, func(i, j int) bool { return sources[i].kind == "alertmanager" && sources[j].kind != "alertmanager" })

	s := section{name: "Alerts"}
	var noRules []string
	for _, src := range sources {
		base := fmt.Sprintf("/api/v1/namespaces/%s/services/%s:%s/proxy", src.ns, src.name, src.port)
		var alerts []firingAlert
		var err error
		if src.kind == "alertmanager" {
			alerts, err = alertmanagerAlerts(cl, ctxName, base)
		} else {
			alerts, err = prometheusAlerts(cl, ctxName, base)
		}
		if err != nil {
			continue
		}
		where := src.ns + "/" + src.name
		if len(alerts) == 0 {
			if src.kind == "prometheus" && !prometheusHasRules(cl, ctxName, base) {
				noRules = append(noRules, where)
				continue
			}
			s.lines = append(s.lines, "none firing"+tui.Dim.Render(" · "+where))
			return s
		}
		s.bad = true
		s.lines = append(s.lines, tui.Err.Render(fmt.Sprintf("%d firing", len(alerts)))+tui.Dim.Render(" · "+where))
		s.lines = append(s.lines, summarizeAlerts(alerts, 5)...)
		return s
	}
	if len(noRules) > 0 {
		s.lines = append(s.lines, tui.Dim.Render("no alerting rules in "+strings.Join(noRules, ", ")+" (alerts may live outside the cluster)"))
		return s
	}
	return section{skip: true}
}

func alertmanagerAlerts(cl kube.Client, ctxName, base string) ([]firingAlert, error) {
	out, err := cl.Get(ctxName, "get", "--raw", base+"/api/v2/alerts?active=true&silenced=false&inhibited=false")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	var out2 []firingAlert
	for _, r := range raw {
		if r.Labels["alertname"] == "Watchdog" || r.Labels["alertname"] == "InfoInhibitor" {
			continue // always-firing heartbeats of kube-prometheus-stack
		}
		out2 = append(out2, firingAlert{r.Labels["alertname"], r.Labels["severity"], firstNonEmpty(r.Annotations["summary"], r.Annotations["description"], r.Annotations["message"])})
	}
	return out2, nil
}

func prometheusAlerts(cl kube.Client, ctxName, base string) ([]firingAlert, error) {
	out, err := cl.Get(ctxName, "get", "--raw", base+"/api/v1/alerts")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Alerts []struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
				State       string            `json:"state"`
			} `json:"alerts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, err
	}
	var alerts []firingAlert
	for _, r := range resp.Data.Alerts {
		if r.State != "firing" || r.Labels["alertname"] == "Watchdog" || r.Labels["alertname"] == "InfoInhibitor" {
			continue
		}
		alerts = append(alerts, firingAlert{r.Labels["alertname"], r.Labels["severity"], firstNonEmpty(r.Annotations["summary"], r.Annotations["description"], r.Annotations["message"])})
	}
	return alerts, nil
}

func prometheusHasRules(cl kube.Client, ctxName, base string) bool {
	out, err := cl.Get(ctxName, "get", "--raw", base+"/api/v1/rules?type=alert")
	if err != nil {
		return true // unknown: don't claim there are none
	}
	var resp struct {
		Data struct {
			Groups []json.RawMessage `json:"groups"`
		} `json:"data"`
	}
	return json.Unmarshal(out, &resp) != nil || len(resp.Data.Groups) > 0
}

// summarizeAlerts groups alerts by name, worst severity first.
func summarizeAlerts(alerts []firingAlert, maxLines int) []string {
	type group struct {
		firingAlert
		n int
	}
	byName := map[string]*group{}
	var order []*group
	for _, a := range alerts {
		g, ok := byName[a.Name]
		if !ok {
			g = &group{firingAlert: a}
			byName[a.Name] = g
			order = append(order, g)
		}
		g.n++
	}
	rank := map[string]int{"critical": 0, "error": 1, "warning": 2}
	sev := func(s string) int {
		if r, ok := rank[s]; ok {
			return r
		}
		return 3
	}
	sort.SliceStable(order, func(i, j int) bool {
		if sev(order[i].Severity) != sev(order[j].Severity) {
			return sev(order[i].Severity) < sev(order[j].Severity)
		}
		return order[i].n > order[j].n
	})
	var lines []string
	for i, g := range order {
		if i == maxLines {
			lines = append(lines, tui.Dim.Render(fmt.Sprintf("… %d more", len(order)-maxLines)))
			break
		}
		name := g.Name
		if g.n > 1 {
			name += fmt.Sprintf(" ×%d", g.n)
		}
		sevText := tui.Dim.Render(g.Severity)
		if g.Severity == "critical" {
			sevText = tui.Err.Render(g.Severity)
		} else if g.Severity == "warning" {
			sevText = tui.Warn.Render(g.Severity)
		}
		lines = append(lines, fmt.Sprintf("%s %s %s", sevText, name, tui.Dim.Render(truncate(oneLine(g.Summary), 80))))
	}
	return lines
}

// ── certificates ───────────────────────────────────────────────────────

func statusCerts(cl kube.Client, ctxName string) section {
	out, err := cl.Get(ctxName, "get", "certificates.cert-manager.io", "-A", "-o", "json")
	if err != nil {
		return section{skip: true} // no cert-manager
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Status struct {
				NotAfter   *time.Time `json:"notAfter"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(out, &list) != nil || len(list.Items) == 0 {
		return section{skip: true}
	}
	s := section{name: "Certs"}
	var problems []string
	for _, c := range list.Items {
		name := c.Metadata.Namespace + "/" + c.Metadata.Name
		for _, cond := range c.Status.Conditions {
			if cond.Type == "Ready" && cond.Status != "True" {
				problems = append(problems, name+" not ready")
			}
		}
		if t := c.Status.NotAfter; t != nil && time.Until(*t) < 14*24*time.Hour {
			problems = append(problems, fmt.Sprintf("%s expires in %s", name, age(time.Now().Add(-time.Until(*t)))))
		}
	}
	first := plural(len(list.Items), "certificate")
	if len(problems) > 0 {
		s.bad = true
		first += " · " + tui.Warn.Render(fmt.Sprintf("%d need attention", len(problems)))
	}
	s.lines = append(s.lines, first)
	if len(problems) > 0 {
		s.lines = append(s.lines, listMore(problems, 4))
	}
	return s
}

// ── warnings ───────────────────────────────────────────────────────────

func statusWarnings(cl kube.Client, ctxName string) section {
	out, err := cl.Get(ctxName, "get", "events", "-A", "--field-selector", "type=Warning", "-o", "json")
	if err != nil {
		return errSection("Warnings", err)
	}
	evs, err := parseEvents(out)
	if err != nil {
		return errSection("Warnings", err)
	}
	byReason := map[string]int{}
	total := 0
	for _, e := range evs {
		if time.Since(e.When) > time.Hour {
			continue
		}
		// one per event object: an event's count is its lifetime total, not the last hour's
		byReason[e.Reason]++
		total++
	}
	s := section{name: "Warnings"}
	if total == 0 {
		s.lines = []string{"none in the last hour"}
		return s
	}
	type rc struct {
		reason string
		n      int
	}
	var rs []rc
	for r, n := range byReason {
		rs = append(rs, rc{r, n})
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].n > rs[j].n || (rs[i].n == rs[j].n && rs[i].reason < rs[j].reason) })
	var parts []string
	for _, r := range rs[:min(len(rs), 5)] {
		parts = append(parts, fmt.Sprintf("%s ×%d", tui.Warn.Render(r.reason), r.n))
	}
	s.hint = "kwarn"
	s.lines = []string{fmt.Sprintf("%d events in the last hour: ", total) + strings.Join(parts, " · ")}
	return s
}

// ── helpers ────────────────────────────────────────────────────────────

func listMore(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, " · ")
	}
	return strings.Join(items[:n], " · ") + tui.Dim.Render(fmt.Sprintf(" · … %d more", len(items)-n))
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
