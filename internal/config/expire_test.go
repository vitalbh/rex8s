package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestForwardExpiry(t *testing.T) {
	for v, want := range map[string]time.Duration{"": 8 * time.Hour, "off": 0, "false": 0, "no": 0, "2h": 2 * time.Hour, "junk": 8 * time.Hour} {
		if got := (&Config{ForwardExpire: v}).ForwardExpiry(); got != want {
			t.Errorf("ForwardExpiry(%q) = %v, want %v", v, got, want)
		}
	}
}

func load(t *testing.T, doc string) *Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REX8S_CONFIG", path)
	c, err := Load()
	if err != nil {
		t.Fatalf("Load(%q): %v", doc, err)
	}
	return c
}

// YAML reads an unquoted off as a boolean: through Load it must still turn
// the expiry off (B4).
func TestForwardExpireThroughLoad(t *testing.T) {
	for doc, want := range map[string]time.Duration{
		"forward_expire: off\n":     0,
		"forward_expire: \"off\"\n": 0,
		"forward_expire: no\n":      0,
		"forward_expire: 0\n":       0,
		"forward_expire: 90m\n":     90 * time.Minute,
		"ai_language: en\n":         8 * time.Hour,
	} {
		c := load(t, doc)
		if got := c.ForwardExpiry(); got != want {
			t.Errorf("%q: ForwardExpiry = %v, want %v", doc, got, want)
		}
		if len(c.Warnings) != 0 {
			t.Errorf("%q: warnings %v", doc, c.Warnings)
		}
	}
}

func TestLoadWarns(t *testing.T) {
	c := load(t, "forward_expire: 2d\nforward_expiry: 1h\ncluster_browser: lens\nmonitor_interval: soon\n"+
		"forwards:\n  - { name: g, service: grafana, port: 3000, prot: 1 }\n  - { name: x }\n"+
		"clusters:\n  - { match: example.com, prd: true }\n")
	if c.ForwardExpiry() != 8*time.Hour {
		t.Errorf("invalid forward_expire: %v", c.ForwardExpiry())
	}
	all := strings.Join(c.Warnings, "\n")
	for _, want := range []string{`forward_expire: "2d"`, "unknown setting forward_expiry", "cluster_browser", "monitor_interval",
		"unknown setting forwards[0].prot", "forwards[1]: name, service and port", "unknown setting clusters[0].prd"} {
		if !strings.Contains(all, want) {
			t.Errorf("no warning with %q in:\n%s", want, all)
		}
	}
	if c := load(t, "log_links: all\ngroups: { prod: [ use, euw ] }\n"); len(c.Warnings) != 0 || c.LogLinksMode() != "all" {
		t.Errorf("valid file: %v %q", c.Warnings, c.LogLinksMode())
	}
}
