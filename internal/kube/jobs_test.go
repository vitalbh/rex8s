package kube

import "testing"

const jobsJSON = `{"items":[
 {"metadata":{"name":"export-1","namespace":"pay","creationTimestamp":"2026-01-01T10:00:00Z","ownerReferences":[{"kind":"CronJob","name":"export"}]},
  "status":{"startTime":"2026-01-01T10:00:00Z","failed":4,"conditions":[{"type":"Failed","status":"True","message":"backoff limit"}]}},
 {"metadata":{"name":"export-0","namespace":"pay","creationTimestamp":"2026-01-01T09:00:00Z","ownerReferences":[{"kind":"CronJob","name":"export"}]},
  "status":{"startTime":"2026-01-01T09:00:00Z","completionTime":"2026-01-01T09:01:40Z","succeeded":1,"conditions":[{"type":"Complete","status":"True"}]}},
 {"metadata":{"name":"migrate","namespace":"api","creationTimestamp":"2026-01-01T10:30:00Z"},
  "status":{"startTime":"2026-01-01T10:30:00Z","active":1}}
]}`

func TestParseJobs(t *testing.T) {
	jobs, err := ParseJobs([]byte(jobsJSON))
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
	if jobs[1].Took() != "1m40s" || jobs[0].Cron != "export" {
		t.Errorf("took %q cron %q", jobs[1].Took(), jobs[0].Cron)
	}
}
