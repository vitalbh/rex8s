package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kcron: what runs in the next hours on every cluster — the CronJobs'
// upcoming runs on one timeline, with how the last run went.
//
//	kcron [DURATION] [FILTER]   default the next 6h; e.g. kcron 24h backup
func (a *app) kcron(args []string) error {
	window, filter := 6*time.Hour, ""
	for _, arg := range args {
		if d, err := time.ParseDuration(arg); err == nil && d > 0 {
			window = d
		} else {
			filter = arg
		}
	}
	// each cluster's CronJobs, and its Jobs for how the last runs went
	type cronSet struct {
		crons []kube.CronJob
		jobs  []kube.Job
	}
	res, err := readAll(a, "CronJobs", [][]string{{"get", "cronjobs", "-A", "-o", "json"}, {"get", "jobs", "-A", "-o", "json"}},
		func(_ kube.Context, out [][]byte) ([]cronSet, error) {
			crons, err := kube.ParseCronJobs(out[0])
			if err != nil {
				return nil, err
			}
			jobs, _ := kube.ParseJobs(out[1])
			return []cronSet{{crons, jobs}}, nil
		})
	if err != nil {
		return err
	}

	now := time.Now()
	type run struct {
		at    time.Time
		c     kube.Context
		cj    kube.CronJob
		state string
	}
	var runs []run
	suspended, bad := 0, 0
	f := strings.ToLower(filter)
	for _, r := range res.Rows {
		for _, cj := range r.V.crons {
			if f != "" && !strings.Contains(strings.ToLower(cj.Namespace+"/"+cj.Name), f) {
				continue
			}
			if cj.Suspend {
				suspended++
				continue
			}
			spec, err := parseCron(cj.Schedule, cj.TimeZone)
			if err != nil {
				bad++
				continue
			}
			state := cronState(cj, r.V.jobs)
			for _, t := range spec.runs(now, now.Add(window), 500) {
				runs = append(runs, run{t, r.C, cj, state})
			}
		}
	}
	sort.SliceStable(runs, func(i, j int) bool {
		if !runs[i].at.Equal(runs[j].at) {
			return runs[i].at.Before(runs[j].at)
		}
		return runs[i].c.Short < runs[j].c.Short
	})

	// a job running every few minutes floods the list: fold its runs
	type key struct{ ctx, ns, name string }
	count := map[key]int{}
	for _, r := range runs {
		count[key{r.c.Name, r.cj.Namespace, r.cj.Name}]++
	}
	const maxShown = 3
	shown := map[key]int{}
	cw, nw := 0, 0
	for _, r := range runs {
		cw = max(cw, len(r.c.Short))
		nw = max(nw, len(r.cj.Namespace)+1+len(r.cj.Name))
	}
	var b strings.Builder
	day := ""
	lines := 0
	for _, r := range runs {
		k := key{r.c.Name, r.cj.Namespace, r.cj.Name}
		shown[k]++
		if shown[k] > maxShown {
			continue
		}
		local := r.at.Local()
		if d := local.Format("Mon 02 Jan"); d != day {
			day = d
			b.WriteString(tui.Bold.Render(d) + "\n")
		}
		cluster := fmt.Sprintf("%-*s", cw, r.c.Short)
		if r.c.Prod {
			cluster = tui.Err.Render(cluster)
		}
		last := ""
		switch r.state {
		case "failed":
			last = tui.Err.Render("last run failed")
		case "running":
			last = tui.Warn.Render("running now")
		}
		more := ""
		if shown[k] == maxShown && count[k] > maxShown {
			more = tui.Dim.Render(fmt.Sprintf(" (+%d more runs)", count[k]-maxShown))
		}
		fmt.Fprintf(&b, "%s  %s  %-*s  %s %s%s\n", tui.Dim.Render(local.Format("15:04")), cluster, nw,
			r.cj.Namespace+"/"+r.cj.Name, tui.Dim.Render(fmt.Sprintf("%-14s", r.cj.Schedule)), last, more)
		lines++
	}
	if lines == 0 {
		b.WriteString(tui.Ok.Render("nothing scheduled in the next "+fmtWindow(window)) + "\n")
	}
	summary := fmt.Sprintf("%d run(s) in the next %s", len(runs), fmtWindow(window))
	if suspended > 0 {
		summary += fmt.Sprintf(" · %d suspended not shown", suspended)
	}
	if bad > 0 {
		summary += fmt.Sprintf(" · %d schedule(s) not understood", bad)
	}
	b.WriteString("\n" + tui.Dim.Render(summary) + "\n")
	fmt.Print(b.String())
	printSkipped(res.Skipped)
	return nil
}
