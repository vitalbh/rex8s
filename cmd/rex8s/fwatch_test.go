package main

import (
	"testing"

	"github.com/vitalbh/rex8s/internal/kube"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

func ks(name, applied, attempted, ready string) kube.FluxItem {
	return kube.FluxItem{Kind: "Kustomization", Namespace: "flux-system", Name: name, Ready: ready,
		AppliedRevision: applied, AttemptedRevision: attempted,
		SourceKind: "GitRepository", SourceNamespace: "flux-system", SourceName: "fleet"}
}

type fakeTracker struct{ url, sha string }

func (f fakeTracker) tracks(r kube.GitRepo) bool               { return kube.NormalizeGitURL(r.URL) == f.url }
func (f fakeTracker) contains(_ kube.GitRepo, rev string) bool { return kube.HasRevision(rev, f.sha) }

func TestEvaluate(t *testing.T) {
	g := fakeTracker{url: kube.NormalizeGitURL("git@github.com:acme/fleet.git"), sha: sha}
	repo := func(rev string) []kube.GitRepo {
		return []kube.GitRepo{{Namespace: "flux-system", Name: "fleet", URL: "https://github.com/acme/fleet", Revision: rev, Ready: "True"}}
	}
	other := []kube.GitRepo{{Namespace: "flux-system", Name: "x", URL: "https://github.com/acme/other"}}
	newRev, oldRev := "main@sha1:"+sha, "main@sha1:fffffff"

	cases := []struct {
		name  string
		repos []kube.GitRepo
		items []kube.FluxItem
		want  watchPhase
	}{
		{"untracked", other, nil, phaseUntracked},
		{"source pending", repo(oldRev), []kube.FluxItem{ks("apps", oldRev, oldRev, "True")}, phaseSource},
		{"applying", repo(newRev), []kube.FluxItem{ks("apps", newRev, newRev, "True"), ks("infra", oldRev, oldRev, "True")}, phaseApplying},
		{"done", repo(newRev), []kube.FluxItem{ks("apps", newRev, newRev, "True")}, phaseDone},
		{"failed", repo(newRev), []kube.FluxItem{ks("apps", oldRev, newRev, "False")}, phaseFailed},
		{"other source ignored", repo(newRev), []kube.FluxItem{{Kind: "Kustomization", Name: "x", SourceKind: "GitRepository", SourceNamespace: "flux-system", SourceName: "x"}}, phaseDone},
	}
	for _, tc := range cases {
		if got := evaluate(tc.repos, tc.items, g); got.phase != tc.want {
			t.Errorf("%s: phase = %d, want %d (%+v)", tc.name, got.phase, tc.want, got)
		}
	}
}

func dep(name string, gen, obs int64, desired, updated, ready int) kube.Workload {
	return kube.Workload{Kind: "Deployment", Namespace: "app", Name: name, Generation: gen, Observed: obs,
		Desired: desired, Updated: updated, Available: ready, KsName: "apps", KsNamespace: "flux-system"}
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
