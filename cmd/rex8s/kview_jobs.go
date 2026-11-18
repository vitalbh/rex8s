package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kview's jobs tab: one row per CronJob (its schedule, how and when its last
// run went, the next one) and per Job of its own, failing first. A
// CronJob's runs — most of a cluster's Jobs — are Hidden rows: enter on the
// CronJob shows them, newest first.

// kvJob is a jobs tab row's object: a Job, or a CronJob with its newest run.
type kvJob struct {
	c    kube.Context
	job  *kube.Job
	cron *kube.CronJob
	last *kube.Job // the CronJob's newest run (nil: none left)
}

const jobHeader = "KIND NAMESPACE/NAME"

var jobTab = kviewTab[jobsAndCrons]{
	view: "jobs", prefix: "job|",
	queries: jobQueries,
	parse:   parseJobsAndCrons,
	rows:    kvJobRows,
}

// kvJobRow is a row with where it sorts.
type kvJobRow struct {
	rank int       // 0 failed, 1 running, 2 CronJobs, 3 Jobs done, 4 a CronJob's runs
	when time.Time // its (last) run: the newest first
	item tui.Item
}

func kvJobRows(k *kview, read []kube.On[jobsAndCrons]) (string, []tui.Item, map[string]any) {
	w, sw := len("NAMESPACE/NAME"), len("SCHEDULE")
	for _, r := range read {
		w = max(w, jobNameWidth(r.V.jobs, r.V.crons))
		for _, cj := range r.V.crons {
			sw = max(sw, min(20, len(cj.Schedule)))
		}
	}
	line := func(kind, ref, schedule, last, next, active string) string {
		return fmt.Sprintf("%-4s %-*s  %-*s  %-14s %-10s %s", kind, w, ref, sw, truncate(schedule, 20), last, next, active)
	}
	header := line("KIND", "NAMESPACE/NAME", "SCHEDULE", "LAST RUN", "NEXT", "ACTIVE")
	now := time.Now()
	objs := map[string]any{}
	var rows []kvJobRow
	for _, r := range read {
		c, prefix := r.C, "job|"+r.C.Name+"|"
		runs := map[string][]*kube.Job{} // namespace/cron → its Jobs
		for i := range r.V.jobs {
			if j := &r.V.jobs[i]; j.Cron != "" {
				runs[j.Namespace+"/"+j.Cron] = append(runs[j.Namespace+"/"+j.Cron], j)
			}
		}
		crons := map[string]bool{} // the CronJobs there are: a Job whose CronJob is gone stands alone
		for i := range r.V.crons {
			cj := &r.V.crons[i]
			ref := cj.Namespace + "/" + cj.Name
			crons[ref] = true
			var last *kube.Job
			active := 0
			for _, j := range runs[ref] {
				if last == nil || j.Start.After(last.Start) {
					last = j
				}
				if j.State == "running" {
					active++
				}
			}
			state := "ok" // as cronState, from the runs found above
			switch {
			case cj.Suspend:
				state = "suspended"
			case last != nil:
				state = last.State
			}
			lastRun, lastText := cj.LastRun, "-"
			if last != nil && last.Start.After(lastRun) {
				lastRun = last.Start
			}
			if !lastRun.IsZero() {
				what := "?" // its Job is gone (history limits)
				if last != nil {
					what = map[string]string{"failed": "failed", "running": "running", "done": "ok"}[last.State]
				}
				lastText = fmt.Sprintf("%-7s %s", what, kube.Age(lastRun))
			}
			key := prefix + "cron/" + ref
			objs[key] = kvJob{c: c, cron: cj, last: last}
			rank := map[string]int{"failed": 0, "running": 1}[state]
			if state != "failed" && state != "running" {
				rank = 2
			}
			lastName := ""
			if last != nil {
				lastName = last.Name
			}
			rows = append(rows, kvJobRow{rank, lastRun, tui.Item{
				Key:   key,
				Label: k.row(c, line("cron", ref, cj.Schedule, lastText, k.next.text(*cj, now), activeText(active))),
				Hint:  kvCronHint(*cj, state, len(runs[ref])),
				Badge: cronBadge(state),
				Name:  kvName(c, "cronjob", ref),
				Sig:   strings.Join([]string{state, lastName, fmt.Sprint(active), fmt.Sprint(cj.Suspend), cj.Schedule}, "|"),
			}})
		}
		for i := range r.V.jobs {
			j := &r.V.jobs[i]
			ref := j.Namespace + "/" + j.Name
			run := j.Cron != "" && crons[j.Namespace+"/"+j.Cron]
			key := prefix + "job/" + ref
			objs[key] = kvJob{c: c, job: j}
			rank := map[string]int{"failed": 0, "running": 1, "done": 3}[j.State]
			if run {
				rank = 4
			}
			what := map[string]string{"failed": "failed", "running": "running", "done": "ok"}[j.State]
			rows = append(rows, kvJobRow{rank, j.Start, tui.Item{
				Key:    key,
				Label:  k.row(c, line("job", ref, "-", fmt.Sprintf("%-7s %s", what, kube.Age(j.Start)), "-", activeText(j.Active))),
				Hint:   kvJobHint(*j),
				Badge:  j.Badge(),
				Name:   kvName(c, "job", ref),
				Sig:    fmt.Sprintf("%s|%d|%d", j.State, j.Active, j.Failed),
				Hidden: run,
			}})
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].rank != rows[j].rank {
			return rows[i].rank < rows[j].rank
		}
		return rows[i].when.After(rows[j].when)
	})
	items := make([]tui.Item, len(rows))
	for i, r := range rows {
		items[i] = r.item
	}
	return header, items, objs
}

func activeText(n int) string {
	if n == 0 {
		return "-"
	}
	return fmt.Sprint(n)
}

// kvCronHint: how many runs are left and, when the last one failed, when
// one last went well.
func kvCronHint(c kube.CronJob, state string, runs int) string {
	var parts []string
	switch runs {
	case 0:
	case 1:
		parts = append(parts, "1 run")
	default:
		parts = append(parts, fmt.Sprintf("%d runs", runs))
	}
	if state == "failed" {
		if c.LastOK.IsZero() {
			parts = append(parts, "never succeeded")
		} else {
			parts = append(parts, "last ok "+kube.Age(c.LastOK)+" ago")
		}
	}
	if c.TimeZone != "" {
		parts = append(parts, c.TimeZone)
	}
	return strings.Join(parts, " · ")
}

// kvJobHint: how long it took, how many tries failed, why.
func kvJobHint(j kube.Job) string {
	parts := []string{"took " + j.Took()}
	if j.State == "running" {
		parts[0] = "running for " + j.Took()
	}
	if j.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed tries", j.Failed))
	}
	if j.Message != "" {
		parts = append(parts, truncate(oneLine(j.Message), 120))
	}
	return strings.Join(parts, " · ")
}

// kvNextRuns remembers each schedule's next run: finding it walks the
// minutes ahead, and the tab reloads every few seconds.
type kvNextRuns struct {
	mu   sync.Mutex
	runs map[string]kvNext // schedule|timeZone →
}

type kvNext struct {
	from, at time.Time // computed at from; at zero: none within cronLookahead
	bad      bool      // the schedule is not understood
}

// cronLookahead is how far NEXT looks: a run further away shows ">7d".
const cronLookahead = 7 * 24 * time.Hour

// text is the NEXT column: "in 4m", "suspended", ">7d", or "?" for a
// schedule not understood.
func (n *kvNextRuns) text(c kube.CronJob, now time.Time) string {
	if c.Suspend {
		return "suspended"
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	key := c.Schedule + "|" + c.TimeZone
	e, ok := n.runs[key]
	if !ok || !e.bad && (e.at.IsZero() && now.Sub(e.from) > time.Hour || !e.at.IsZero() && !now.Before(e.at)) {
		e = kvNext{from: now}
		if spec, err := parseCron(c.Schedule, c.TimeZone); err != nil {
			e.bad = true
		} else if at := spec.runs(now, now.Add(cronLookahead), 1); len(at) > 0 {
			e.at = at[0]
		}
		if n.runs == nil {
			n.runs = map[string]kvNext{}
		}
		n.runs[key] = e
	}
	switch {
	case e.bad:
		return "?"
	case e.at.IsZero():
		return ">7d"
	}
	if d := e.at.Sub(now); d >= time.Minute {
		return "in " + kube.Age(time.Now().Add(-d))
	}
	return "in <1m"

}

// runsOf accepts the Jobs CronJob ns/name of cluster c started.
func (k *kview) runsOf(c kube.Context, ns, name string) func(tui.Item) bool {
	return func(it tui.Item) bool {
		o, ok := k.obj(it.Key)
		j, isJ := o.(kvJob)
		return ok && isJ && j.job != nil && j.c.Name == c.Name && j.job.Namespace == ns && j.job.Cron == name
	}
}
