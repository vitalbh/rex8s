package main

import (
	"strings"
	"testing"

	"github.com/vitalbh/rex8s/internal/kube"
)

func TestImageTag(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/acme/api:1.4.2": "1.4.2",
		"nginx":                  "latest",
		"localhost:5000/web":     "latest",
		"localhost:5000/web:2.0": "2.0",
		"ghcr.io/acme/api@sha256:0123456789abcdef0123": "@0123456789ab",
		"": "?",
	}
	for in, want := range cases {
		if got := imageTag(in); got != want {
			t.Errorf("imageTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVersionRows(t *testing.T) {
	use := kube.Context{Short: "use", Prod: true}
	stg := kube.Context{Short: "staging"}
	w := func(c kube.Context, name, img string, extra ...kube.Container) target {
		return target{c: c, Workload: kube.Workload{Kind: "Deployment", Namespace: "app", Name: name,
			Containers: append(extra, kube.Container{Name: name, Image: img})}}
	}
	rows := versionRows([]target{
		w(use, "web", "acme/web:3.8.1"), w(stg, "web", "acme/web:3.8.1"),
		// the sidecar comes first; the main container is the one named like the workload
		w(use, "api", "acme/api:1.42.0", kube.Container{Name: "proxy", Image: "envoy:1.30"}), w(stg, "api", "acme/api:1.43.0"),
	})
	if len(rows) != 2 || rows[0].Name != "api" || !rows[0].differs() || rows[1].differs() {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Tags["use"] != "1.42.0" || rows[0].Tags["staging"] != "1.43.0" {
		t.Errorf("api tags = %v", rows[0].Tags)
	}
	out := renderVersions(rows, []kube.Context{use, stg})
	if !strings.Contains(out, "1 differ") {
		t.Errorf("summary missing:\n%s", out)
	}
}
