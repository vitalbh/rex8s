package main

import (
	"fmt"
	"strconv"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kjobsAll: kjobs -A — the Jobs and CronJobs of every cluster in one picker,
// failed first wherever they are. Same preview and actions as kjobs.
func (a *app) kjobsAll(state, filter string) error {
	reads, skipped, err := a.readAllClusters("jobs",
		[]string{"get", "jobs", "-A", "-o", "json"}, []string{"get", "cronjobs", "-A", "-o", "json"})
	if err != nil {
		return err
	}
	type parsed struct {
		c     kube.Context
		jobs  []job
		crons []cronJob
	}
	var ps []parsed
	wc, w := 0, 0
	for _, r := range reads {
		jobs, err := parseJobs(r.out[0])
		if err != nil {
			skipped = append(skipped, r.c.Short+": "+err.Error())
			continue
		}
		crons, err := parseCronJobs(r.out[1])
		if err != nil {
			skipped = append(skipped, r.c.Short+": "+err.Error())
			continue
		}
		ps = append(ps, parsed{r.c, jobs, crons})
		wc = max(wc, len(r.c.Short))
		w = max(w, jobNameWidth(jobs, crons))
	}
	printSkipped(skipped)

	jobsByKey, cronsByKey, cronStates := map[string]job{}, map[string]cronJob{}, map[string]string{}
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
	title := clustersTitle("Jobs", len(ps), skipped)
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
