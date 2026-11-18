package main

import (
	"os"
	"testing"
)

func TestKgitHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:Acme/GitOps.git":            "github.com/acme/gitops",
		"https://github.com/Acme/GitOps":            "github.com/acme/gitops",
		"ssh://git@github.com/Acme/GitOps.git":      "github.com/acme/gitops",
		"ssh://git@gitlab.example.com:2222/a/b.git": "gitlab.example.com/a/b",
	} {
		if got := normRepo(in); got != want {
			t.Errorf("normRepo(%q) = %q, want %q", in, got, want)
		}
	}
	dir := t.TempDir()
	os.MkdirAll(dir+"/clusters/dal/apps", 0o755)
	os.WriteFile(dir+"/clusters/dal/apps/api.yaml", []byte("apiVersion: v1\nkind: Service\nmetadata:\n  name: api\n---\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n  namespace: app\n"), 0o644)
	hits := findDefinitions(dir, fluxChain{path: "./clusters/dal", lookKind: "Deployment", lookName: "api"})
	if len(hits) != 1 || hits[0].rel != "clusters/dal/apps/api.yaml" || hits[0].line != 9 {
		t.Fatalf("hits = %+v", hits)
	}
	if u := webURL("git@github.com:org/gitops.git", "main", "clusters/dal/apps/api.yaml", false); u != "https://github.com/org/gitops/blob/main/clusters/dal/apps/api.yaml" {
		t.Errorf("webURL = %s", u)
	}
}
