package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

func jobHint(j kube.Job) string {
	h := ""
	switch j.State {
	case "failed":
		h = fmt.Sprintf("failed   %d tries · %s ago", j.Failed, kube.Age(j.Start))
	case "running":
		h = fmt.Sprintf("running  for %s", j.Took())
	default:
		h = fmt.Sprintf("done     in %s · %s ago", j.Took(), kube.Age(j.Start))
	}
	if j.Cron != "" {
		h += " · cron " + j.Cron
	}
	return h
}

// state of a CronJob from its newest Job (its own status has no result).
func cronState(c kube.CronJob, jobs []kube.Job) string {
	if c.Suspend {
		return "suspended"
	}
	var last *kube.Job
	for i := range jobs {
		j := &jobs[i]
		if j.Cron == c.Name && j.Namespace == c.Namespace && (last == nil || j.Start.After(last.Start)) {
			last = j
		}
	}
	if last == nil {
		return "ok"
	}
	return last.State
}

func cronBadge(state string) string {
	switch state {
	case "suspended":
		return "SUSP"
	case "failed":
		return "FAIL"
	case "running":
		return "WAIT"
	}
	return "ok"
}

func cronHint(c kube.CronJob, state string) string {
	h := fmt.Sprintf("%-14s", c.Schedule)
	if !c.LastRun.IsZero() {
		h += " · last run " + kube.Age(c.LastRun) + " ago"
	}
	if state == "failed" {
		if c.LastOK.IsZero() {
			h += " · never succeeded"
		} else {
			h += " · last ok " + kube.Age(c.LastOK) + " ago"
		}
	}
	if c.Suspend {
		h += " · suspended"
	}
	return h
}

var jobStates = map[string]bool{"failed": true, "running": true, "done": true, "suspended": true, "cron": true}

// kjobs: Jobs and CronJobs of the cluster (all namespaces; -n this pane's
// namespace). An optional state keeps only failed | running | done |
// suspended | cron; any other word is the initial filter. Preview: events and
// last log lines of a Job, recent runs of a CronJob. Enter: actions (run now,
// suspend/resume, logs, delete…); changes go through the production guard.
// -A: every cluster.
func (a *app) kjobs(args []string) error {
	allNS, state, filter, every := true, "", "", false
	for _, arg := range args {
		switch {
		case arg == "-A":
			every = true
		case arg == "-n":
			allNS = false
		case jobStates[arg]:
			state = arg
		default:
			filter = arg
		}
	}
	if every {
		return a.kjobsAll(state, filter)
	}
	c, err := kube.Current(a.cfg, "", "")
	if err != nil {
		return err
	}
	return a.kjobsCluster(c, allNS, state, filter)
}

// kjobsOn: kjobs on cluster c (every namespace), filtered.
func (a *app) kjobsOn(c kube.Context, filter string) error {
	return a.kjobsCluster(c, true, "", filter)
}

func (a *app) kjobsCluster(c kube.Context, allNS bool, state, filter string) error {
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	ns := c.Namespace
	if ns == "" {
		ns = "default"
	}
	scope, where := []string{"-A"}, c.Short
	if !allNS && !c.AllNS {
		scope, where = []string{"-n", ns}, c.Short+"/"+ns
	}

	var jobs []kube.Job
	ix := newJobIndex()
	title := "Jobs on " + where
	if state != "" {
		title += " · " + state
	}
	it, err := tui.Run(tui.Options{
		Title: title,
		Query: filter,
		Load: func() ([]tui.Item, error) {
			jout, err := cl.Get(c.Name, append([]string{"get", "jobs", "-o", "json"}, scope...)...)
			if err != nil {
				return nil, err
			}
			if jobs, err = kube.ParseJobs(jout); err != nil {
				return nil, err
			}
			cout, err := cl.Get(c.Name, append([]string{"get", "cronjobs", "-o", "json"}, scope...)...)
			if err != nil {
				return nil, err
			}
			crons, err := kube.ParseCronJobs(cout)
			if err != nil {
				return nil, err
			}
			return jobItems(jobs, crons, state, ix)
		},
		PreviewHead: func(it tui.Item) string {
			if cj, ok := ix.crons[it.Key]; ok {
				return cronHead(cj, ix.states[it.Key])
			}
			return jobHead(ix.jobs[it.Key])
		},
		PreviewBody: func(it tui.Item) string {
			if cj, ok := ix.crons[it.Key]; ok {
				return cronRuns(cj, jobs)
			}
			return jobBody(cl, c.Name, ix.jobs[it.Key])
		},
	})
	if err != nil || it == nil {
		return err
	}
	if cj, ok := ix.crons[it.Key]; ok {
		return a.cronAction(c, cj)
	}
	return a.jobAction(c, cl, ix.jobs[it.Key])
}

func jobItems(jobs []kube.Job, crons []kube.CronJob, state string, ix jobIndex) ([]tui.Item, error) {
	rows := jobRows(jobRowsIn{jobs: jobs, crons: crons, state: state, width: jobNameWidth(jobs, crons)}, ix)
	if len(rows) == 0 {
		if state != "" {
			return nil, fmt.Errorf("no %s jobs", state)
		}
		return nil, fmt.Errorf("no jobs or cronjobs")
	}
	sortJobRows(rows)
	items := make([]tui.Item, len(rows))
	for i, r := range rows {
		items[i] = r.item
	}
	return items, nil
}

type jobRow struct {
	rank int
	when time.Time
	item tui.Item
}

func jobNameWidth(jobs []kube.Job, crons []kube.CronJob) int {
	w := 0
	for _, j := range jobs {
		w = max(w, len(j.Namespace)+1+len(j.Name))
	}
	for _, cj := range crons {
		w = max(w, len(cj.Namespace)+1+len(cj.Name))
	}
	return w
}

// jobIndex finds, by picker key, the Job or CronJob behind each row and
// the CronJob's state, for previews and actions.
type jobIndex struct {
	jobs   map[string]kube.Job
	crons  map[string]kube.CronJob
	states map[string]string // cron key → cronState
}

func newJobIndex() jobIndex {
	return jobIndex{jobs: map[string]kube.Job{}, crons: map[string]kube.CronJob{}, states: map[string]string{}}
}

// jobRowsIn: the Jobs and CronJobs of one cluster, and how their rows look.
type jobRowsIn struct {
	jobs  []kube.Job
	crons []kube.CronJob
	state string // failed, running, done, suspended or cron; "" all

	keyPrefix, labelPrefix string // several clusters in one list
	width                  int    // of the name column
}

// jobRows: one row per Job and CronJob that passes in.state, each recorded
// in ix; the prefixes tell clusters apart in kjobs -A and kview -A.
func jobRows(in jobRowsIn, ix jobIndex) []jobRow {
	jobs, crons, state, w := in.jobs, in.crons, in.state, in.width
	var rows []jobRow
	if state != "cron" && state != "suspended" {
		for _, j := range jobs {
			if state != "" && j.State != state {
				continue
			}
			key := in.keyPrefix + "job/" + j.Namespace + "/" + j.Name
			ix.jobs[key] = j
			rank := map[string]int{"failed": 0, "running": 1, "done": 3}[j.State]
			rows = append(rows, jobRow{rank, j.Start, tui.Item{Key: key, Label: in.labelPrefix + fmt.Sprintf("job  %-*s", w, j.Namespace+"/"+j.Name), Hint: jobHint(j), Badge: j.Badge()}})
		}
	}
	if state == "" || state == "cron" || state == "suspended" {
		for _, cj := range crons {
			st := cronState(cj, jobs)
			if state == "suspended" && !cj.Suspend {
				continue
			}
			key := in.keyPrefix + "cron/" + cj.Namespace + "/" + cj.Name
			ix.crons[key], ix.states[key] = cj, st
			rows = append(rows, jobRow{2, cj.LastRun, tui.Item{Key: key, Label: in.labelPrefix + fmt.Sprintf("cron %-*s", w, cj.Namespace+"/"+cj.Name), Hint: cronHint(cj, st), Badge: cronBadge(st)}})
		}
	}
	return rows
}

// sortJobRows: failed, running, cronjobs, done; the most recent first.
func sortJobRows(rows []jobRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].rank != rows[j].rank {
			return rows[i].rank < rows[j].rank
		}
		return rows[i].when.After(rows[j].when)
	})
}

func jobHead(j kube.Job) string {
	st := tui.Ok.Render(j.State)
	switch j.State {
	case "failed":
		st = tui.Err.Render(j.State)
	case "running":
		st = tui.Warn.Render(j.State)
	}
	lines := []string{tui.Bold.Render(j.Name) + "  " + st, "",
		tui.Dim.Render("namespace ") + j.Namespace,
		tui.Dim.Render("started   ") + kube.Age(j.Start) + " ago",
		tui.Dim.Render("took      ") + j.Took()}
	if j.Cron != "" {
		lines = append(lines, tui.Dim.Render("cronjob   ")+j.Cron)
	}
	if j.Failed > 0 {
		lines = append(lines, tui.Dim.Render("failures  ")+fmt.Sprint(j.Failed))
	}
	if j.Message != "" {
		lines = append(lines, tui.Dim.Render("reason    ")+j.Message)
	}
	return strings.Join(lines, "\n")
}

func jobBody(cl kube.Client, ctxName string, j kube.Job) string {
	parts := []string{objectEvents(cl, ctxName, j.Namespace, "Job", j.Name, 4)}
	out, err := cl.Get(ctxName, "logs", "-n", j.Namespace, "job/"+j.Name, fmt.Sprintf("--tail=%d", failLogLines))
	switch {
	case err != nil:
		parts = append(parts, tui.Dim.Render("no logs: "+lastLogLine(err.Error())))
	case strings.TrimSpace(string(out)) == "":
		parts = append(parts, tui.Dim.Render("no logs"))
	default:
		parts = append(parts, tui.Bold.Render("logs"), logTail(string(out)))
	}
	return strings.Join(parts, "\n\n")
}

func cronHead(c kube.CronJob, state string) string {
	lines := []string{tui.Bold.Render(c.Name) + "  " + tui.Dim.Render("CronJob"), "",
		tui.Dim.Render("namespace ") + c.Namespace,
		tui.Dim.Render("schedule  ") + c.Schedule,
		tui.Dim.Render("state     ") + state}
	if !c.LastRun.IsZero() {
		lines = append(lines, tui.Dim.Render("last run  ")+kube.Age(c.LastRun)+" ago")
	}
	if !c.LastOK.IsZero() {
		lines = append(lines, tui.Dim.Render("last ok   ")+kube.Age(c.LastOK)+" ago")
	}
	return strings.Join(lines, "\n")
}

// cronRuns lists the CronJob's most recent Jobs.
func cronRuns(c kube.CronJob, jobs []kube.Job) string {
	var runs []kube.Job
	for _, j := range jobs {
		if j.Cron == c.Name && j.Namespace == c.Namespace {
			runs = append(runs, j)
		}
	}
	if len(runs) == 0 {
		return tui.Dim.Render("no recent runs (finished Jobs are cleaned up by the history limits)")
	}
	sort.Slice(runs, func(i, k int) bool { return runs[i].Start.After(runs[k].Start) })
	lines := []string{tui.Bold.Render("recent runs")}
	for _, j := range runs[:min(len(runs), 10)] {
		st := tui.Ok.Render(fmt.Sprintf("%-8s", j.State))
		switch j.State {
		case "failed":
			st = tui.Err.Render(fmt.Sprintf("%-8s", j.State))
		case "running":
			st = tui.Warn.Render(fmt.Sprintf("%-8s", j.State))
		}
		lines = append(lines, fmt.Sprintf("%s %-5s %s  %s", st, kube.Age(j.Start), tui.Dim.Render("took "+j.Took()), j.Name))
	}
	return strings.Join(lines, "\n")
}

func (a *app) jobAction(c kube.Context, cl kube.Client, j kube.Job) error {
	actions := []tui.Item{
		{Key: "logs", Label: fmt.Sprintf("%-9s", "logs"), Hint: "follow the logs in a split"},
		{Key: "describe", Label: fmt.Sprintf("%-9s", "describe"), Hint: "kubectl describe"},
		{Key: "events", Label: fmt.Sprintf("%-9s", "events"), Hint: "all events of the job"},
	}
	if j.Cron != "" {
		actions = append(actions, tui.Item{Key: "rerun", Label: fmt.Sprintf("%-9s", "rerun"), Hint: "new Job from cronjob/" + j.Cron})
	}
	actions = append(actions, tui.Item{Key: "delete", Label: fmt.Sprintf("%-9s", "delete"), Hint: "kubectl delete job (and its pods)"})
	act, err := tui.Run(tui.Options{Title: fmt.Sprintf("job %s/%s on %s", j.Namespace, j.Name, c.Short), Items: actions})
	if err != nil || act == nil {
		return err
	}
	switch act.Key {
	case "logs":
		if rex.Block() == "" {
			return a.logtail(c.Name, j.Namespace, "job/"+j.Name, "", logOpts{})
		}
		return rex.SplitBehind("logs "+j.Name, filepath.Join(a.binDir, "rex8s"), "_logs", c.Name, j.Namespace, "job/"+j.Name)
	case "describe":
		return a.runKubectl(false, "--context", c.Name, "-n", j.Namespace, "describe", "job", j.Name)
	case "events":
		fmt.Println(objectEvents(cl, c.Name, j.Namespace, "Job", j.Name, 0))
	case "rerun":
		return a.runCron(c, j.Namespace, j.Cron)
	case "delete":
		return a.runKubectl(true, "--context", c.Name, "-n", j.Namespace, "delete", "job", j.Name)
	}
	return nil
}

func (a *app) cronAction(c kube.Context, cj kube.CronJob) error {
	toggle, toggleHint := "suspend", "stop scheduling new runs"
	if cj.Suspend {
		toggle, toggleHint = "resume", "schedule runs again"
	}
	actions := []tui.Item{
		{Key: "run", Label: fmt.Sprintf("%-9s", "run now"), Hint: "kubectl create job --from=cronjob/" + cj.Name},
		{Key: "jobs", Label: fmt.Sprintf("%-9s", "jobs"), Hint: "only this cronjob's jobs"},
		{Key: toggle, Label: fmt.Sprintf("%-9s", toggle), Hint: toggleHint},
		{Key: "describe", Label: fmt.Sprintf("%-9s", "describe"), Hint: "kubectl describe"},
	}
	act, err := tui.Run(tui.Options{Title: fmt.Sprintf("cronjob %s/%s on %s", cj.Namespace, cj.Name, c.Short), Items: actions})
	if err != nil || act == nil {
		return err
	}
	switch act.Key {
	case "run":
		return a.runCron(c, cj.Namespace, cj.Name)
	case "jobs":
		return a.kjobsOn(c, cj.Name)
	case "suspend", "resume":
		patch := fmt.Sprintf(`{"spec":{"suspend":%v}}`, act.Key == "suspend")
		return a.runKubectl(true, "--context", c.Name, "-n", cj.Namespace, "patch", "cronjob", cj.Name, "--type", "merge", "-p", patch)
	case "describe":
		return a.runKubectl(false, "--context", c.Name, "-n", cj.Namespace, "describe", "cronjob", cj.Name)
	}
	return nil
}

// runCron starts a Job from a CronJob now (through the production guard).
func (a *app) runCron(c kube.Context, ns, cron string) error {
	name := manualJobName(cron, time.Now())
	if err := a.runKubectl(true, "--context", c.Name, "-n", ns, "create", "job", name, "--from=cronjob/"+cron); err != nil {
		return err
	}
	fmt.Println(tui.Dim.Render("follow it: kjobs " + name))
	return nil
}

// manualJobName: <cron>-manual-<unix time>, cut to the 63-char name limit.
func manualJobName(cron string, now time.Time) string {
	suffix := fmt.Sprintf("-manual-%d", now.Unix())
	if len(cron)+len(suffix) > 63 {
		cron = strings.TrimRight(cron[:63-len(suffix)], "-")
	}
	return cron + suffix
}
