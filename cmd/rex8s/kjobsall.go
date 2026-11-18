package main

import (
	"fmt"
	"strconv"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// jobQueries reads a cluster's Jobs and CronJobs, for parseJobsAndCrons.
var jobQueries = [][]string{{"get", "jobs", "-A", "-o", "json"}, {"get", "cronjobs", "-A", "-o", "json"}}

// jobsAndCrons is one cluster's Jobs and CronJobs.
type jobsAndCrons struct {
	jobs  []kube.Job
	crons []kube.CronJob
}

// parseJobsAndCrons parses jobQueries' answers as a single row.
func parseJobsAndCrons(_ kube.Context, out [][]byte) ([]jobsAndCrons, error) {
	jobs, err := kube.ParseJobs(out[0])
	if err != nil {
		return nil, err
	}
	crons, err := kube.ParseCronJobs(out[1])
	if err != nil {
		return nil, err
	}
	return []jobsAndCrons{{jobs, crons}}, nil
}

// kjobsAll: kjobs -A — the Jobs and CronJobs of every cluster in one picker,
// failed first wherever they are. Same preview and actions as kjobs.
func (a *app) kjobsAll(state, filter string) error {
	res, err := readAll(a, "jobs", jobQueries, parseJobsAndCrons)
	if err != nil {
		return err
	}
	type parsed struct {
		c     kube.Context
		jobs  []kube.Job
		crons []kube.CronJob
	}
	var ps []parsed
	wc, w := 0, 0
	for _, r := range res.Rows {
		ps = append(ps, parsed{r.C, r.V.jobs, r.V.crons})
		wc = max(wc, len(r.C.Short))
		w = max(w, jobNameWidth(r.V.jobs, r.V.crons))
	}
	printSkipped(res.Skipped)

	jobsByKey, cronsByKey, cronStates := map[string]kube.Job{}, map[string]kube.CronJob{}, map[string]string{}
	clusterOf := map[string]int{} // key → index in ps
	var rows []jobRow
	for i, p := range ps {
		prefix := strconv.Itoa(i) + "|"
		for _, r := range jobRows(p.jobs, p.crons, state, prefix, fmt.Sprintf("%-*s  ", wc, p.c.Short), w, jobsByKey, cronsByKey, cronStates) {
			clusterOf[r.item.Key] = i
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		if state != "" {
			return fmt.Errorf("no %s jobs on any cluster", state)
		}
		return fmt.Errorf("no jobs or cronjobs on any cluster")
	}
	sortJobRows(rows)
	items := make([]tui.Item, len(rows))
	for i, r := range rows {
		items[i] = r.item
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	title := clustersTitle("Jobs", len(ps), len(res.Skipped))
	if state != "" {
		title += " · " + state
	}
	it, err := tui.Run(tui.Options{
		Title: title,
		Items: items,
		Query: filter,
		PreviewHead: func(it tui.Item) string {
			if cj, ok := cronsByKey[it.Key]; ok {
				return cronHead(cj, cronStates[it.Key])
			}
			return jobHead(jobsByKey[it.Key])
		},
		PreviewBody: func(it tui.Item) string {
			p := ps[clusterOf[it.Key]]
			if cj, ok := cronsByKey[it.Key]; ok {
				return cronRuns(cj, p.jobs)
			}
			return jobBody(cl, p.c.Name, jobsByKey[it.Key])
		},
	})
	if err != nil || it == nil {
		return err
	}
	c := ps[clusterOf[it.Key]].c
	if cj, ok := cronsByKey[it.Key]; ok {
		return a.cronAction(c, cj)
	}
	return a.jobAction(c, cl, jobsByKey[it.Key])
}
