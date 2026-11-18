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

func TestMatchWords(t *testing.T) {
	for _, c := range []struct {
		text, filter string
		want         bool
	}{
		{"use monitoring/grafana", "grafana", true},
		{"use monitoring/grafana", "Grafana USE", true},
		{"use monitoring/grafana", "grafana euw", false},
		{"use monitoring/grafana", "", true},
	} {
		if got := matchWords(c.text, c.filter); got != c.want {
			t.Errorf("matchWords(%q, %q) = %v", c.text, c.filter, got)
		}
	}
}
