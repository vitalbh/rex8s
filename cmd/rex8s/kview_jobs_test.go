package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kvJobsFixture: export (*/30, its last run failed, two runs), nightly
// (suspended, one run ok), a Job of its own that failed, one done, and a
// run of a CronJob that is gone (it stands alone).
func kvJobsFixture() (jobs, crons string) {
	ago := func(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339) }
	job := func(name, cron string, start time.Duration, status string) string {
		owner := ""
		if cron != "" {
			owner = fmt.Sprintf(`,"ownerReferences":[{"kind":"CronJob","name":%q}]`, cron)
		}
		return fmt.Sprintf(`{"metadata":{"name":%q,"namespace":"default","creationTimestamp":%q%s},"status":{"startTime":%q,%s}}`,
			name, ago(start), owner, ago(start), status)
	}
	failed := `"failed":3,"conditions":[{"type":"Failed","status":"True","message":"BackoffLimitExceeded"}]`
	done := `"succeeded":1,"conditions":[{"type":"Complete","status":"True"}]`
	jobs = `{"items":[` + strings.Join([]string{
		job("export-2", "export", 10*time.Minute, failed),
		job("export-1", "export", 40*time.Minute, done),
		job("nightly-1", "nightly", 9*time.Hour, done),
		job("migrate", "", 2*time.Hour, failed),
		job("backfill", "", 3*time.Hour, done),
		job("old-cron-1", "old-cron", time.Hour, done),
	}, ",") + `]}`
	crons = `{"items":[
	 {"metadata":{"name":"export","namespace":"default"},"spec":{"schedule":"*/30 * * * *"},"status":{"lastScheduleTime":"` + ago(10*time.Minute) + `"}},
	 {"metadata":{"name":"nightly","namespace":"default"},"spec":{"schedule":"0 3 * * *","suspend":true}}]}`
	return jobs, crons
}

// The jobs tab lists CronJobs and the Jobs of their own, failing first; a
// CronJob's runs are hidden rows that enter shows, newest first.
func TestKviewJobsGroupedUnderCronJobs(t *testing.T) {
	t.Setenv("REX_BLOCK", "")
	jobs, crons := kvJobsFixture()
	k := fakeKview(t, map[string]string{kvDev.Name + "-jobs.json": jobs, kvDev.Name + "-cronjobs.json": crons})
	k.clusters, k.multi = []kube.Context{kvDev}, false
	items := load(t, jobTab.loader(k))

	var shown []string
	for _, it := range items {
		if !it.Hidden {
			shown = append(shown, it.Name)
		}
	}
	want := []string{
		"cronjob default/export on use",  // its last run failed
		"job default/migrate on use",     // failed
		"cronjob default/nightly on use", // suspended
		"job default/old-cron-1 on use",  // its CronJob is gone: on its own
		"job default/backfill on use",
	}
	if !slices.Equal(shown, want) {
		t.Fatalf("rows\n %q\nwant\n %q", shown, want)
	}
	export, _ := rowOn(items, "cronjob default/export")
	if l := export.Label; !strings.Contains(l, "*/30 * * * *") || !strings.Contains(l, "failed  10m") || !strings.Contains(l, "in ") ||
		export.Badge != "FAIL" || !strings.Contains(export.Hint, "2 runs") {
		t.Errorf("export row: %q · %q · %s", l, export.Hint, export.Badge)
	}
	if nightly, _ := rowOn(items, "cronjob default/nightly"); !strings.Contains(nightly.Label, "suspended") || nightly.Badge != "SUSP" {
		t.Errorf("nightly row: %q %s", nightly.Label, nightly.Badge)
	}
	if !strings.HasPrefix(k.header("jobs", jobHeader)(), "KIND NAMESPACE/NAME") || !strings.Contains(k.header("jobs", jobHeader)(), "NEXT") {
		t.Errorf("header %q", k.header("jobs", jobHeader)())
	}

	// enter: its runs, newest first; esc comes back (the browser's Goto)
	r := k.action(vJobs, export, "enter")
	if r.Goto != vJobs+1 || r.OnlyLabel != "runs of cronjob default/export" ||
		!slices.Equal(onlyNames(items, r.Only), []string{"job default/export-2 on use", "job default/export-1 on use"}) {
		t.Fatalf("runs: %+v %v", r, onlyNames(items, r.Only))
	}
	// l: the last run's logs
	r = k.action(vJobs, export, "l")
	if r.Exec == nil || !slices.Equal(r.Exec.Args[1:], []string{"_logs", kvDev.Name, "default", "job/export-2"}) {
		t.Errorf("logs of the last run: %+v", r)
	}
	// a run: enter shows its pods
	run, _ := rowOn(items, "job default/export-2")
	if r = k.action(vJobs, run, "enter"); r.Goto != vPods+1 || r.Only == nil || !strings.Contains(r.OnlyLabel, "pods of job default/export-2") {
		t.Errorf("job → pods: %+v", r)
	}
}

// Sig changes with the state, not with time going by.
func TestKviewJobsSig(t *testing.T) {
	jobs, crons := kvJobsFixture()
	k := fakeKview(t, map[string]string{kvDev.Name + "-jobs.json": jobs, kvDev.Name + "-cronjobs.json": crons})
	k.clusters, k.multi = []kube.Context{kvDev}, false
	sigs := func(items []tui.Item) map[string]string {
		out := map[string]string{}
		for _, it := range items {
			out[it.Key] = it.Sig
		}
		return out
	}
	a := sigs(load(t, jobTab.loader(k)))
	b := sigs(load(t, jobTab.loader(k)))
	for key, s := range a {
		if s == "" || b[key] != s {
			t.Errorf("%s: sig %q then %q", key, s, b[key])
		}
	}
}

func TestKvNextRuns(t *testing.T) {
	var n kvNextRuns
	now := time.Date(2026, 11, 17, 10, 7, 30, 0, time.UTC)
	cases := []struct {
		cj   kube.CronJob
		want string
	}{
		{kube.CronJob{Schedule: "*/15 * * * *"}, "in 7m"},
		{kube.CronJob{Schedule: "8 * * * *"}, "in <1m"},
		{kube.CronJob{Schedule: "0 3 * * *", Suspend: true}, "suspended"},
		{kube.CronJob{Schedule: "0 0 1 1 *"}, ">7d"},
		{kube.CronJob{Schedule: "61 * * * *"}, "?"},
	}
	for _, c := range cases {
		if got := n.text(c.cj, now); got != c.want {
			t.Errorf("%s: %q, want %q", c.cj.Schedule, got, c.want)
		}
	}
	// remembered until it passes
	if e := n.runs["*/15 * * * *|"]; !e.at.Equal(time.Date(2026, 11, 17, 10, 15, 0, 0, time.UTC)) {
		t.Fatalf("cached %v", e.at)
	}
	if got := n.text(kube.CronJob{Schedule: "*/15 * * * *"}, now.Add(10*time.Minute)); got != "in 12m" {
		t.Errorf("after the run: %q", got)
	}
}
