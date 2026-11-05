package main

import (
	"strings"
	"testing"
	"time"
)

const jobsJSON = `{"items":[
 {"metadata":{"name":"export-1","namespace":"pay","creationTimestamp":"2026-01-01T10:00:00Z","ownerReferences":[{"kind":"CronJob","name":"export"}]},
  "status":{"startTime":"2026-01-01T10:00:00Z","failed":4,"conditions":[{"type":"Failed","status":"True","message":"backoff limit"}]}},
 {"metadata":{"name":"export-0","namespace":"pay","creationTimestamp":"2026-01-01T09:00:00Z","ownerReferences":[{"kind":"CronJob","name":"export"}]},
  "status":{"startTime":"2026-01-01T09:00:00Z","completionTime":"2026-01-01T09:01:40Z","succeeded":1,"conditions":[{"type":"Complete","status":"True"}]}},
 {"metadata":{"name":"migrate","namespace":"api","creationTimestamp":"2026-01-01T10:30:00Z"},
  "status":{"startTime":"2026-01-01T10:30:00Z","active":1}}
]}`

const cronsJSON = `{"items":[
 {"metadata":{"name":"export","namespace":"pay"},"spec":{"schedule":"*/30 * * * *"},"status":{"lastScheduleTime":"2026-01-01T10:00:00Z","lastSuccessfulTime":"2026-01-01T09:01:40Z"}},
 {"metadata":{"name":"sitemap","namespace":"web"},"spec":{"schedule":"0 */6 * * *","suspend":true},"status":{}}
]}`

func TestParseJobs(t *testing.T) {
	jobs, err := parseJobs([]byte(jobsJSON))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, j := range jobs {
		got[j.Name] = j.State
	}
	want := map[string]string{"export-1": "failed", "export-0": "done", "migrate": "running"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: state %q, want %q", k, got[k], v)
		}
	}
	if jobs[1].took() != "1m40s" || jobs[0].Cron != "export" {
		t.Errorf("took %q cron %q", jobs[1].took(), jobs[0].Cron)
	}
}

func TestCronStateAndItems(t *testing.T) {
	jobs, _ := parseJobs([]byte(jobsJSON))
	crons, err := parseCronJobs([]byte(cronsJSON))
	if err != nil {
		t.Fatal(err)
	}
	if s := cronState(crons[0], jobs); s != "failed" {
		t.Errorf("export: %q, want failed (newest job failed)", s)
	}
	if s := cronState(crons[1], jobs); s != "suspended" {
		t.Errorf("sitemap: %q, want suspended", s)
	}

	items, err := jobItems(jobs, crons, "", map[string]job{}, map[string]cronJob{}, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, it := range items {
		order = append(order, it.Key)
	}
	want := "job/pay/export-1 job/api/migrate cron/pay/export cron/web/sitemap job/pay/export-0"
	if strings.Join(order, " ") != want {
		t.Errorf("order:\n got %s\nwant %s", strings.Join(order, " "), want)
	}

	for state, n := range map[string]int{"failed": 1, "running": 1, "done": 1, "cron": 2, "suspended": 1} {
		items, err := jobItems(jobs, crons, state, map[string]job{}, map[string]cronJob{}, map[string]string{})
		if err != nil || len(items) != n {
			t.Errorf("state %s: %d items (%v), want %d", state, len(items), err, n)
		}
	}
}

func TestManualJobName(t *testing.T) {
	now := time.Unix(1700000000, 0)
	if got := manualJobName("export", now); got != "export-manual-1700000000" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("a", 60)
	if got := manualJobName(long, now); len(got) > 63 {
		t.Errorf("%q is %d chars", got, len(got))
	}
}
