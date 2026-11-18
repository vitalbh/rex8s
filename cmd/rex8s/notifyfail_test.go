package main

import (
	"testing"
)

func TestPodWorkload(t *testing.T) {
	for in, want := range map[string]string{
		"api-7d9f6c5b8-4kq2x":          "api",
		"payments-api-6f7c8d9b5-kz4lp": "payments-api",
		"redis-0":                      "redis",
		"node-exporter-7xk2m":          "node-exporter",
		"nightly-sync-29871540-mbk4c":  "nightly-sync-29871540",
	} {
		if got := podWorkload(in); got != want {
			t.Errorf("podWorkload(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewBroken(t *testing.T) {
	cs := []clusterFailingJSON{{Context: "use", Pods: []podJSON{{Namespace: "api", Name: "worker-5c8b7d4f9-hx2lp", Status: "CrashLoopBackOff"}}}}
	now := brokenKeys(cs)
	if got := newBroken(nil, now); got != nil {
		t.Errorf("first run should not notify: %v", got)
	}
	if got := newBroken(map[string]bool{}, now); len(got) != 1 || now[got[0]] != "api/worker CrashLoopBackOff" {
		t.Errorf("new = %v", got)
	}
	// the crash-looping pod recreated under a new name: same workload, not new
	cs[0].Pods[0].Name = "worker-5c8b7d4f9-zz9qq"
	seen := map[string]bool{}
	for k := range now {
		seen[k] = true
	}
	if got := newBroken(seen, brokenKeys(cs)); len(got) != 0 {
		t.Errorf("recreated pod notified again: %v", got)
	}
}
