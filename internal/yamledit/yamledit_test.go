package yamledit

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/yaml"
)

func TestStringRoundTrips(t *testing.T) {
	for _, s := range []string{"grafana", "off", "OFF", "on", "yes", "no", "y", "n", "true", "False", "null", "~",
		"8h", "2m", "0", "123", "1.5", "1e3", "0x1f", "1_000", "2026-01-02", ".inf", "", " x", "a: b", "#2b1216",
		"-x", "~/dev/gitops", "pt-BR", "a,b", "it's"} {
		doc := "v: " + String(s) + "\n"
		var m struct {
			V string `json:"v"`
		}
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil || m.V != s {
			t.Errorf("String(%q) = %q reads back as %q (%v)", s, String(s), m.V, err)
		}
	}
	for s, want := range map[string]string{"grafana": "grafana", "off": `"off"`, "8h": "8h", "3000": `"3000"`, "a: b": `"a: b"`, "~/dev/x": "~/dev/x"} {
		if got := String(s); got != want {
			t.Errorf("String(%q) = %q, want %q", s, got, want)
		}
	}
}

func TestSetKey(t *testing.T) {
	doc := "kubeconfig: ~/.kube/config\nprod_background: \"#2b1216\"   # tint\nhide: []\n"
	got := SetKey(doc, "prod_background", `"#1f1430"`)
	if !strings.Contains(got, "prod_background: \"#1f1430\"") || !strings.Contains(got, "# tint") || !strings.Contains(got, "hide: []") || strings.Contains(got, "2b1216") {
		t.Errorf("replace:\n%s", got)
	}
	if got := SetKey("hide: []", "prod_background", `"off"`); got != "hide: []\nprod_background: \"off\"\n" {
		t.Errorf("append: %q", got)
	}
}

func TestAddListItem(t *testing.T) {
	item := "{ name: grafana, cluster: use, namespace: monitoring, service: grafana, port: 3000 }"
	cases := map[string]string{
		"":                        "forwards:\n  - " + item + "\n",
		"hide: []\n":              "hide: []\nforwards:\n  - " + item + "\n",
		"forwards: []\nhide: x\n": "forwards:\n  - " + item + "\nhide: x\n",
		"forwards:\n  - { name: a }\n# note\nhide: x\n": "forwards:\n  - { name: a }\n  - " + item + "\n# note\nhide: x\n",
		// inline lists with items become block lists (B16)
		"forwards: [ { name: a, port: 1 }, { name: b, port: 2 } ]\nhide: x\n": "forwards:\n  - { name: a, port: 1 }\n  - { name: b, port: 2 }\n  - " + item + "\nhide: x\n",
		"forwards: [ { name: a }, \"x, y\" ]   # favs\n":                      "forwards:                                  # favs\n  - { name: a }\n  - \"x, y\"\n  - " + item + "\n",
		"forwards: [\n  { name: a },\n  { name: b }\n]\nhide: x\n":            "forwards:\n  - { name: a }\n  - { name: b }\n  - " + item + "\nhide: x\n",
	}
	for in, want := range cases {
		got := AddListItem(in, "forwards", item)
		if got != want {
			t.Errorf("AddListItem(%q)\n got %q\nwant %q", in, got, want)
		}
		var m map[string]any
		if err := yaml.Unmarshal([]byte(got), &m); err != nil {
			t.Errorf("AddListItem(%q) is not valid YAML: %v\n%s", in, err, got)
		}
	}
}

func TestFlowAndBlockLists(t *testing.T) {
	doc := "ai_language: pt-BR\ngitops_repos: [ ~/dev/GitOps ]   # repos\nhide:\n  - \"old\"\n  - other   # note\nlogin_url: x\n"
	got := AddFlowItem(doc, "gitops_repos", "~/dev/Infra")
	if !strings.Contains(got, "gitops_repos: [ ~/dev/GitOps, ~/dev/Infra ]   # repos") {
		t.Errorf("add flow:\n%s", got)
	}
	if AddFlowItem(got, "gitops_repos", "~/dev/Infra") != got {
		t.Error("add flow duplicated")
	}
	if got := RemoveListItem(doc, "gitops_repos", "~/dev/GitOps"); !strings.Contains(got, "gitops_repos: [  ]") {
		t.Errorf("remove flow:\n%s", got)
	}
	if got := RemoveListItem(doc, "hide", "other"); strings.Contains(got, "other") || !strings.Contains(got, `"old"`) || !strings.Contains(got, "login_url: x") {
		t.Errorf("remove block:\n%s", got)
	}
	if got := RemoveKey(doc, "hide"); strings.Contains(got, "hide") || strings.Contains(got, "old") || !strings.Contains(got, "login_url: x") {
		t.Errorf("unset:\n%s", got)
	}
}

func TestEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.yaml")
	parses := func() error {
		data, _ := os.ReadFile(path)
		var m map[string]any
		return yaml.Unmarshal(data, &m)
	}
	// a missing file starts empty
	res, err := Edit(path, func(doc string) string { return SetKey(doc, "ai_language", "en") }, parses)
	if err != nil || !res.Changed {
		t.Fatalf("create: %+v %v", res, err)
	}
	if res, err := Edit(path, func(doc string) string { return doc }, parses); err != nil || res.Changed {
		t.Fatalf("no change: %+v %v", res, err)
	}
	// the user saves the file meanwhile
	_, err = Edit(path, func(doc string) string {
		future := time.Now().Add(time.Hour)
		os.Chtimes(path, future, future)
		return SetKey(doc, "ai_language", "pt-BR")
	}, parses)
	if !errors.Is(err, ErrChanged) {
		t.Errorf("concurrent edit: %v", err)
	}
	// a change that breaks the file is undone
	before, _ := os.ReadFile(path)
	if _, err := Edit(path, func(doc string) string { return doc + "clusters: [ {\n" }, parses); err == nil || !strings.Contains(err.Error(), "undone") {
		t.Errorf("broken YAML: %v", err)
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("not restored: %q", after)
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("temporary file left behind")
	}
}
