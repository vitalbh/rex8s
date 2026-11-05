package main

import (
	"strings"
	"testing"

	"github.com/vitalbh/rex8s/internal/kube"
)

func dep(name string, gen, obs int64, desired, updated, ready int) kube.Workload {
	return kube.Workload{Kind: "Deployment", Namespace: "app", Name: name, Generation: gen, Observed: obs,
		Desired: desired, Updated: updated, Ready: ready, KsName: "apps", KsNamespace: "flux-system"}
}

func TestEvaluateRollout(t *testing.T) {
	ks := map[string]bool{"flux-system/apps": true}
	baseline := map[string]baselineEntry{
		"Deployment/app/api": {generation: 4}, "Deployment/app/web": {generation: 7},
		"Deployment/app/broken": {generation: 2}, "Deployment/app/stuck": {generation: 3, failed: true},
	}
	stuck := dep("stuck", 3, 3, 2, 1, 1) // rollout stuck before we started: ignored
	stuck.Failed = true
	broken := dep("broken", 2, 2, 1, 1, 0) // fully updated but unhealthy before we started: ignored

	cases := []struct {
		name string
		ws   []kube.Workload
		want watchPhase
	}{
		{"nothing touched", []kube.Workload{dep("api", 4, 4, 2, 2, 2), broken, stuck}, phaseDone},
		{"rolling", []kube.Workload{dep("api", 5, 5, 2, 1, 1), dep("web", 7, 7, 3, 3, 3), broken}, phaseRollout},
		{"rolled out", []kube.Workload{dep("api", 5, 5, 2, 2, 2), broken}, phaseDone},
		{"new workload rolling", []kube.Workload{dep("new", 1, 1, 1, 1, 0)}, phaseRollout},
		{"other kustomization ignored", []kube.Workload{{Kind: "Deployment", Name: "x", Generation: 9, KsName: "other", KsNamespace: "flux-system"}}, phaseDone},
	}
	for _, tc := range cases {
		if got := evaluateRollout(tc.ws, baseline, ks); got.phase != tc.want {
			t.Errorf("%s: phase = %d, want %d (%+v)", tc.name, got.phase, tc.want, got)
		}
	}

	failed := dep("api", 5, 5, 2, 1, 1)
	failed.Failed, failed.Message = true, "ReplicaSet api-x has timed out progressing."
	if got := evaluateRollout([]kube.Workload{failed}, baseline, ks); got.phase != phaseFailed {
		t.Fatalf("deadline exceeded must fail: %+v", got)
	}
}

func TestStreamWarningsDedup(t *testing.T) {
	ev := `{"metadata":{"uid":"u1","namespace":"app"},"involvedObject":{"kind":"Pod","name":"api-1"},"reason":"BackOff","message":"Back-off restarting failed container","count":5,"lastTimestamp":"2026-11-04T10:00:00Z"}`
	ev2 := `{"metadata":{"uid":"u2","namespace":"app"},"involvedObject":{"kind":"Pod","name":"api-2"},"reason":"Unhealthy","message":"Readiness probe failed","count":1,"lastTimestamp":"2026-11-04T10:00:01Z"}`
	var out strings.Builder
	streamWarnings(strings.NewReader(ev+"\n"+ev+"\n"+ev2+"\n"), &out, true)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "app pod/api-1") || !strings.Contains(lines[0], "x5") {
		t.Fatalf("got:\n%s", out.String())
	}
}

func TestRedactEnv(t *testing.T) {
	in := `Containers:
  app:
    Image:  api:1
    Environment:
      DB_PASSWORD:  hunter2
      API_KEY:      <set to the key 'key' in secret 'api'>  Optional: false
      POD_IP:        (v1:status.podIP)
    Mounts:
      /data from data (rw)`
	out := redactEnv(in)
	if strings.Contains(out, "hunter2") || !strings.Contains(out, "DB_PASSWORD: <redacted>") ||
		!strings.Contains(out, "<set to the key 'key' in secret 'api'>") || !strings.Contains(out, "/data from data") {
		t.Fatalf("redactEnv:\n%s", out)
	}
}
