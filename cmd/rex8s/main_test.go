package main

import (
	"strings"
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

func TestDiffKeys(t *testing.T) {
	added, removed := diffKeys(map[string]int{"a": 1, "b": 1}, map[string]int{"b": 1, "c": 1})
	if len(added) != 1 || added[0] != "c" || len(removed) != 1 || removed[0] != "a" {
		t.Fatalf("added=%v removed=%v", added, removed)
	}
}

func TestDedupeByServer(t *testing.T) {
	out := dedupe([]kube.Context{{Name: "a", Server: "s1"}, {Name: "a-alias", Server: "s1"}, {Name: "b", Server: "s2"}})
	if len(out) != 2 || out[0].Name != "a" || out[1].Name != "b" {
		t.Fatalf("%+v", out)
	}
}

func TestPodBadge(t *testing.T) {
	ps, err := parsePods([]byte(`{"items":[
	 {"metadata":{"name":"ok"},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running","containerStatuses":[{"ready":true}]}},
	 {"metadata":{"name":"crash"},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running","containerStatuses":[{"ready":false,"restartCount":7,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].badge() != "ok" || ps[1].badge() != "FAIL" || ps[1].Restarts != 7 || ps[1].status() != "CrashLoopBackOff" {
		t.Fatalf("%+v", ps)
	}
}

func TestParseServicesTCPOnly(t *testing.T) {
	ss, err := parseServices([]byte(`{"items":[
	 {"metadata":{"name":"web"},"spec":{"type":"ClusterIP","ports":[{"name":"http","port":80},{"name":"dns","port":53,"protocol":"UDP"}]}},
	 {"metadata":{"name":"udp-only"},"spec":{"ports":[{"port":53,"protocol":"UDP"}]}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || len(ss[0].Ports) != 1 || ss[0].Ports[0].Port != 80 || ss[0].portsText() != "80/http" {
		t.Fatalf("%+v", ss)
	}
}

func TestFreePort(t *testing.T) {
	if p := freePort(0); p <= 0 {
		t.Fatalf("freePort(0) = %d", p)
	}
}

func TestShellSplit(t *testing.T) {
	got, err := shellSplit(`kubectl config set-credentials "my user" --auth-provider-arg='id-token=a b' --x=c\ d`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"kubectl", "config", "set-credentials", "my user", "--auth-provider-arg=id-token=a b", "--x=c d"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if _, err := shellSplit(`kubectl "open`); err == nil {
		t.Fatal("unterminated quote must fail")
	}
}
