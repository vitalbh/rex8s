package kube

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vitalbh/rex8s/internal/config"
)

const fixture = `apiVersion: v1
kind: Config
current-context: a
clusters:
- name: ca
  cluster: {server: "https://k8s.us-east.example.com:6443"}
- name: cb
  cluster: {server: "https://other:6443"}
users:
- name: ua
  user: {token: aaa}
- name: ub
  user: {token: bbb}
contexts:
- name: a
  context: {cluster: ca, user: ua}
- name: "b with space "
  context: {cluster: cb, user: ub, namespace: ops}
`

func TestWritePane(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "config")
	if err := os.WriteFile(src, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	pane := filepath.Join(dir, "cache", "blk.yaml")
	if err := WritePane(src, "b with space ", "api", pane); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(pane)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v, want 0600", fi.Mode().Perm())
	}
	raw, err := Load(pane)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.Clusters) != 0 || len(raw.AuthInfos) != 0 {
		t.Fatalf("pane file must not contain clusters/credentials: %d clusters, %d users", len(raw.Clusters), len(raw.AuthInfos))
	}
	if raw.CurrentContext != "b with space " || raw.Contexts["b with space "].Namespace != "api" {
		t.Fatalf("current=%q", raw.CurrentContext)
	}

	// merge pane:master — the pane wins (ns), the master provides cluster/user
	t.Setenv("KUBECONFIG", PaneKubeconfig(pane, src))
	cfg := &config.Config{Clusters: []config.Cluster{{Match: "k8s.us-east.", Name: "use", Prod: true}}}
	c, err := Current(cfg, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "b with space " || c.Namespace != "api" || c.Server != "https://other:6443" || c.User != "ub" {
		t.Fatalf("Current merged = %+v", c)
	}

	if err := SetNamespace(pane, "web"); err != nil {
		t.Fatal(err)
	}
	if c, _ = Current(cfg, "", ""); c.Namespace != "web" {
		t.Fatalf("SetNamespace: ns = %q", c.Namespace)
	}
	if err := WritePane(src, "a", "", pane); err != nil { // kctx
		t.Fatal(err)
	}
	if c, _ = Current(cfg, "", ""); c.Name != "a" || !c.Prod || c.Short != "use" {
		t.Fatalf("after switch: %+v", c)
	}
	// master untouched
	if m, _ := Load(src); m.Contexts["b with space "].Namespace != "ops" {
		t.Fatal("the master kubeconfig was modified")
	}

	all, _ := Load(src)
	ctxs := Contexts(all, &config.Config{Hide: []string{"b with space "}, Clusters: cfg.Clusters})
	if len(ctxs) != 1 || !ctxs[0].Prod || ctxs[0].Short != "use" {
		t.Fatalf("Contexts = %+v", ctxs)
	}
}

func TestParseFlux(t *testing.T) {
	data := []byte(`{"items":[
	 {"kind":"Kustomization","metadata":{"name":"apps","namespace":"flux-system"},"status":{"conditions":[{"type":"Ready","status":"True"}]}},
	 {"kind":"HelmRelease","metadata":{"name":"api","namespace":"api"},"status":{"conditions":[{"type":"Ready","status":"False","message":"upgrade retries exhausted"}]}},
	 {"kind":"HelmRelease","metadata":{"name":"old","namespace":"x"},"spec":{"suspend":true}},
	 {"kind":"Kustomization","metadata":{"name":"new","namespace":"y"}}
	]}`)
	st, err := ParseFlux(data)
	if err != nil {
		t.Fatal(err)
	}
	if st.Ready != 1 || st.Failing != 1 || st.Suspended != 1 || st.Progressing != 1 {
		t.Fatalf("%+v", st)
	}
	if len(st.Problems) != 2 || st.Problems[0].Name != "api" || !st.Problems[1].Suspended {
		t.Fatalf("problems = %+v", st.Problems)
	}
}
