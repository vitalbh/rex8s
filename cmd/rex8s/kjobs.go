package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

type job struct {
	Namespace, Name, Cron string
	Start, End            time.Time
	Active, Failed        int
	State                 string // failed, running, done
	Message               string
}

type cronJob struct {
	Namespace, Name, Schedule string
	TimeZone                  string // spec.timeZone ("" = the controller's, usually UTC)
	Suspend                   bool
	LastRun, LastOK           time.Time
}

func parseJobs(data []byte) ([]job, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string    `json:"name"`
				Namespace         string    `json:"namespace"`
				CreationTimestamp time.Time `json:"creationTimestamp"`
				OwnerReferences   []struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
			Status struct {
				StartTime      *time.Time `json:"startTime"`
				CompletionTime *time.Time `json:"completionTime"`
				Active         int        `json:"active"`
				Succeeded      int        `json:"succeeded"`
				Failed         int        `json:"failed"`
				Conditions     []struct {
					Type               string     `json:"type"`
					Status             string     `json:"status"`
					Message            string     `json:"message"`
					LastTransitionTime *time.Time `json:"lastTransitionTime"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]job, 0, len(list.Items))
	for _, it := range list.Items {
		j := job{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, Start: it.Metadata.CreationTimestamp,
			Active: it.Status.Active, Failed: it.Status.Failed, State: "running"}
		if it.Status.StartTime != nil {
			j.Start = *it.Status.StartTime
		}
		if it.Status.CompletionTime != nil {
			j.End = *it.Status.CompletionTime
		}
		for _, o := range it.Metadata.OwnerReferences {
			if o.Kind == "CronJob" {
				j.Cron = o.Name
			}
		}
		for _, c := range it.Status.Conditions {
			if c.Status != "True" {
				continue
			}
			switch c.Type {
			case "Failed":
				j.State, j.Message = "failed", c.Message
				if c.LastTransitionTime != nil && j.End.IsZero() {
					j.End = *c.LastTransitionTime
				}
			case "Complete":
				j.State = "done"
			}
		}
		if j.State == "running" && it.Status.Active == 0 && it.Status.Succeeded > 0 {
			j.State = "done"
		}
		out = append(out, j)
	}
	return out, nil
}

func parseCronJobs(data []byte) ([]cronJob, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Schedule string `json:"schedule"`
				TimeZone string `json:"timeZone"`
				Suspend  bool   `json:"suspend"`
			} `json:"spec"`
			Status struct {
				LastScheduleTime   *time.Time `json:"lastScheduleTime"`
				LastSuccessfulTime *time.Time `json:"lastSuccessfulTime"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]cronJob, 0, len(list.Items))
	for _, it := range list.Items {
		c := cronJob{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, Schedule: it.Spec.Schedule, TimeZone: it.Spec.TimeZone, Suspend: it.Spec.Suspend}
		if t := it.Status.LastScheduleTime; t != nil {
			c.LastRun = *t
		}
		if t := it.Status.LastSuccessfulTime; t != nil {
			c.LastOK = *t
		}
		out = append(out, c)
	}
	return out, nil
}

func (j job) badge() string {
	switch j.State {
	case "failed":
		return "FAIL"
	case "running":
		return "WAIT"
	}
	return "done"
}

func (j job) took() string {
	switch {
	case j.Start.IsZero(), j.End.IsZero() && j.State == "failed":
		return "?"
	case j.End.IsZero():
		return time.Since(j.Start).Round(time.Second).String()
	}
	return j.End.Sub(j.Start).Round(time.Second).String()
}

func (j job) hint() string {
	h := ""
	switch j.State {
	case "failed":
		h = fmt.Sprintf("failed   %d tries · %s ago", j.Failed, age(j.Start))
	case "running":
		h = fmt.Sprintf("running  for %s", j.took())
	default:
		h = fmt.Sprintf("done     in %s · %s ago", j.took(), age(j.Start))
	}
	if j.Cron != "" {
		h += " · cron " + j.Cron
	}
	return h
}

// state of a CronJob from its newest Job (its own status has no result).
func cronState(c cronJob, jobs []job) string {
	if c.Suspend {
		return "suspended"
	}
	var last *job
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

func (c cronJob) hint(state string) string {
	h := fmt.Sprintf("%-14s", c.Schedule)
	if !c.LastRun.IsZero() {
		h += " · last run " + age(c.LastRun) + " ago"
	}
	if state == "failed" {
		if c.LastOK.IsZero() {
			h += " · never succeeded"
		} else {
			h += " · last ok " + age(c.LastOK) + " ago"
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
func (a *app) kjobs(args []string) error {
	allNS, state, filter := true, "", ""
	for _, arg := range args {
		switch {
		case arg == "-n":
			allNS = false
		case jobStates[arg]:
			state = arg
		default:
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
	ns := c.Namespace
	if ns == "" {
		ns = "default"
	}
	scope, where := []string{"-A"}, c.Short
	if !allNS && !c.AllNS {
		scope, where = []string{"-n", ns}, c.Short+"/"+ns
	}

	var jobs []job
	jobsByKey, cronsByKey, cronStates := map[string]job{}, map[string]cronJob{}, map[string]string{}
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
			if jobs, err = parseJobs(jout); err != nil {
				return nil, err
			}
			cout, err := cl.Get(c.Name, append([]string{"get", "cronjobs", "-o", "json"}, scope...)...)
			if err != nil {
				return nil, err
			}
			crons, err := parseCronJobs(cout)
			if err != nil {
				return nil, err
			}
			return jobItems(jobs, crons, state, jobsByKey, cronsByKey, cronStates)
		},
		PreviewHead: func(it tui.Item) string {
			if cj, ok := cronsByKey[it.Key]; ok {
				return cronHead(cj, cronStates[it.Key])
			}
			return jobHead(jobsByKey[it.Key])
		},
		PreviewBody: func(it tui.Item) string {
			if cj, ok := cronsByKey[it.Key]; ok {
				return cronRuns(cj, jobs)
			}
			return jobBody(cl, c.Name, jobsByKey[it.Key])
		},
	})
	if err != nil || it == nil {
		return err
	}
	if cj, ok := cronsByKey[it.Key]; ok {
		return a.cronAction(c, cj)
	}
	return a.jobAction(c, cl, jobsByKey[it.Key])
}

func jobItems(jobs []job, crons []cronJob, state string, jobsByKey map[string]job, cronsByKey map[string]cronJob, cronStates map[string]string) ([]tui.Item, error) {
	type row struct {
		rank int
		when time.Time
		item tui.Item
	}
	var rows []row
	w := 0
	for _, j := range jobs {
		w = max(w, len(j.Namespace)+1+len(j.Name))
	}
	for _, cj := range crons {
		w = max(w, len(cj.Namespace)+1+len(cj.Name))
	}
	if state != "cron" && state != "suspended" {
		for _, j := range jobs {
			if state != "" && j.State != state {
				continue
			}
			key := "job/" + j.Namespace + "/" + j.Name
			jobsByKey[key] = j
			rank := map[string]int{"failed": 0, "running": 1, "done": 3}[j.State]
			rows = append(rows, row{rank, j.Start, tui.Item{Key: key, Label: fmt.Sprintf("job  %-*s", w, j.Namespace+"/"+j.Name), Hint: j.hint(), Badge: j.badge()}})
		}
	}
	if state == "" || state == "cron" || state == "suspended" {
		for _, cj := range crons {
			st := cronState(cj, jobs)
			if state == "suspended" && !cj.Suspend {
				continue
			}
			key := "cron/" + cj.Namespace + "/" + cj.Name
			cronsByKey[key], cronStates[key] = cj, st
			rows = append(rows, row{2, cj.LastRun, tui.Item{Key: key, Label: fmt.Sprintf("cron %-*s", w, cj.Namespace+"/"+cj.Name), Hint: cj.hint(st), Badge: cronBadge(st)}})
		}
	}
	if len(rows) == 0 {
		if state != "" {
			return nil, fmt.Errorf("no %s jobs", state)
		}
		return nil, fmt.Errorf("no jobs or cronjobs")
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
	return items, nil
}

func jobHead(j job) string {
	st := tui.Ok.Render(j.State)
	switch j.State {
	case "failed":
		st = tui.Err.Render(j.State)
	case "running":
		st = tui.Warn.Render(j.State)
	}
	lines := []string{tui.Bold.Render(j.Name) + "  " + st, "",
		tui.Dim.Render("namespace ") + j.Namespace,
		tui.Dim.Render("started   ") + age(j.Start) + " ago",
		tui.Dim.Render("took      ") + j.took()}
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

func jobBody(cl kube.Client, ctxName string, j job) string {
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

func cronHead(c cronJob, state string) string {
	lines := []string{tui.Bold.Render(c.Name) + "  " + tui.Dim.Render("CronJob"), "",
		tui.Dim.Render("namespace ") + c.Namespace,
		tui.Dim.Render("schedule  ") + c.Schedule,
		tui.Dim.Render("state     ") + state}
	if !c.LastRun.IsZero() {
		lines = append(lines, tui.Dim.Render("last run  ")+age(c.LastRun)+" ago")
	}
	if !c.LastOK.IsZero() {
		lines = append(lines, tui.Dim.Render("last ok   ")+age(c.LastOK)+" ago")
	}
	return strings.Join(lines, "\n")
}

// cronRuns lists the CronJob's most recent Jobs.
func cronRuns(c cronJob, jobs []job) string {
	var runs []job
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
		lines = append(lines, fmt.Sprintf("%s %-5s %s  %s", st, age(j.Start), tui.Dim.Render("took "+j.took()), j.Name))
	}
	return strings.Join(lines, "\n")
}

func (a *app) jobAction(c kube.Context, cl kube.Client, j job) error {
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
		return rex.SplitBehind("logs "+j.Name, filepath.Join(a.binDir, "rex8s"), "logtail", c.Name, j.Namespace, "job/"+j.Name)
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

func (a *app) cronAction(c kube.Context, cj cronJob) error {
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
		return a.kjobs([]string{cj.Name})
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
