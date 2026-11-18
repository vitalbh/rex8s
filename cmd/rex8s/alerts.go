package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
)

type alertJSON struct {
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Where    string `json:"where"`
}

type clusterAlertsJSON struct {
	Context      string      `json:"context"`
	Short        string      `json:"short"`
	Prod         bool        `json:"prod"`
	Error        string      `json:"error,omitempty"`
	LoginExpired bool        `json:"loginExpired,omitempty"`
	Source       string      `json:"source"`            // ns/service read, "" when the cluster has none
	NoRules      bool        `json:"noRules,omitempty"` // a Prometheus without alerting rules
	Alerts       []alertJSON `json:"alerts"`
}

var severityRank = map[string]int{"critical": 0, "page": 0, "error": 1, "high": 1, "warning": 2, "medium": 2, "info": 3, "low": 3, "none": 4}

func sevRank(s string) int {
	if r, ok := severityRank[strings.ToLower(s)]; ok {
		return r
	}
	return 3
}

// alertsAll reads the firing alerts of every cluster, in parallel (rex8s
// list alerts, the Raycast menu bar).
func (a *app) alertsAll() ([]clusterAlertsJSON, error) {
	raw, err := kube.Load(a.src)
	if err != nil {
		return nil, err
	}
	clusters := fleet.All(raw, a.cfg)
	cl, err := a.client(a.src)
	if err != nil {
		return nil, err
	}
	cl.Timeout = 20 * time.Second
	// no fleet query: the reader only probes each cluster (an unreachable one
	// fails fast), clusterAlerts does the reading; every cluster gets a row
	res := fleet.Read(a.readerFor(cl), clusters, nil, func(c kube.Context, _ [][]byte) ([]clusterAlertsJSON, error) {
		return []clusterAlertsJSON{clusterAlerts(cl, c)}, nil
	})
	byName := map[string]clusterAlertsJSON{}
	for _, r := range res.Rows {
		byName[r.C.Name] = r.V
	}
	for _, s := range res.Skipped {
		byName[s.Cluster.Name] = clusterAlertsJSON{Error: s.Reason, LoginExpired: s.Expired}
	}
	out := make([]clusterAlertsJSON, len(clusters))
	for i, c := range clusters {
		j := byName[c.Name]
		j.Context, j.Short, j.Prod = c.Name, c.Short, c.Prod
		if j.Alerts == nil {
			j.Alerts = []alertJSON{}
		}
		out[i] = j
	}
	return out, nil
}

// clusterAlerts reads c's firing alerts from the first alert source that
// answers.
func clusterAlerts(cl kube.Client, c kube.Context) clusterAlertsJSON {
	var j clusterAlertsJSON
	// whoami tells an expired login apart; its other errors (an API server
	// too old for it) do not matter here
	if _, err := cl.Get(c.Name, "auth", "whoami", "-o", "name"); err != nil && kube.AuthExpired(err) {
		j.Error, j.LoginExpired = lastLogLine(err.Error()), true
		return j
	}
	for _, src := range alertSources(cl, c.Name) {
		as, err := src.fetch(cl, c.Name)
		if err != nil {
			j.Error = lastLogLine(err.Error())
			continue
		}
		j.Error, j.Source = "", src.ns+"/"+src.name
		if len(as) == 0 && src.kind == "prometheus" && !prometheusHasRules(cl, c.Name, src.base()) {
			j.NoRules = true
			continue // maybe another source has them
		}
		j.NoRules = false
		for _, al := range as {
			j.Alerts = append(j.Alerts, alertJSON{al.Name, al.Severity, oneLine(al.Summary), al.Where})
		}
		sort.SliceStable(j.Alerts, func(x, y int) bool { return sevRank(j.Alerts[x].Severity) < sevRank(j.Alerts[y].Severity) })
		break
	}
	return j
}

// notifyNewCritical: a macOS notification per cluster for critical alerts
// that started firing since the last check (first run only records).
func (a *app) notifyNewCritical(cs []clusterAlertsJSON) {
	path := filepath.Join(a.cache, "alerts-seen.json")
	var seen map[string]bool
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &seen)
		if seen == nil {
			seen = map[string]bool{}
		}
	}
	now := map[string]string{}
	for _, c := range cs {
		for _, al := range c.Alerts {
			if sevRank(al.Severity) > 0 {
				continue
			}
			now[c.Context+"|"+al.Name+"|"+al.Where] = strings.TrimSpace(al.Name + " " + al.Where)
		}
	}
	next := map[string]bool{}
	for _, c := range cs { // a cluster we couldn't read keeps its state
		if c.Source == "" {
			for k := range seen {
				if strings.HasPrefix(k, c.Context+"|") {
					next[k] = true
				}
			}
		}
	}
	for k := range now {
		next[k] = true
	}
	if data, err := json.Marshal(next); err == nil {
		_ = os.MkdirAll(a.cache, 0o700)
		_ = os.WriteFile(path, data, 0o600)
	}
	byCluster := map[string][]string{}
	for _, k := range newBroken(seen, now) {
		ctx := strings.SplitN(k, "|", 2)[0]
		byCluster[ctx] = append(byCluster[ctx], now[k])
	}
	for _, c := range cs {
		if msgs := byCluster[c.Context]; len(msgs) > 0 {
			rex.Notify(fmt.Sprintf("rex8s · %s: %d critical alert(s)", c.Short, len(msgs)), strings.Join(msgs[:min(len(msgs), 3)], " · "))
		}
	}
}
