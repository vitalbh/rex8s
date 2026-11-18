package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLookup(t *testing.T) {
	c := &Config{Clusters: []Cluster{
		{Match: "k8s.us-east.example.com", Name: "use", Prod: true},
		{Match: "legacy.example.com", Name: "legacy"},
	}}
	cases := []struct {
		server, ctx, short string
		prod               bool
	}{
		{"https://k8s.us-east.example.com:6443", "acme-prod-use", "use", true},
		{"https://k8s.us-east.example.com:6443", "Prod US East ", "use", true}, // alias inherits prod
		{"https://legacy.example.com:6443", "Legacy", "legacy", false},
		{"https://other:6443", "my cluster ", "my_cluster", false},
	}
	for _, tc := range cases {
		s, p := c.Lookup(tc.server, tc.ctx)
		if s != tc.short || p != tc.prod {
			t.Errorf("Lookup(%q,%q) = %q,%v; want %q,%v", tc.server, tc.ctx, s, p, tc.short, tc.prod)
		}
	}
}

func TestHiddenExact(t *testing.T) {
	c := &Config{Hide: []string{"Prod US East "}}
	if !c.Hidden("Prod US East ") || c.Hidden("Prod US East") {
		t.Fatal("Hidden must compare the exact name, including trailing space")
	}
}

func TestKubeconfigPathList(t *testing.T) {
	h, _ := os.UserHomeDir()
	c := &Config{Kubeconfig: "~/.kube/config: ~/.kube/lab.yaml"}
	want := filepath.Join(h, ".kube/config") + string(os.PathListSeparator) + filepath.Join(h, ".kube/lab.yaml")
	if got := c.KubeconfigPath(); got != want {
		t.Errorf("KubeconfigPath() = %q, want %q", got, want)
	}
}

func TestGuardAndWrappers(t *testing.T) {
	var none *Config
	if !(&Config{}).GuardOn() || !(&Config{}).WrappersOn() || !none.GuardOn() || !none.WrappersOn() {
		t.Fatal("guard and wrappers are on by default")
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("guard: false\nwrappers: false\nskip_wrappers: [kns, kctx]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REX8S_CONFIG", p)
	c, err := Load()
	if err != nil || len(c.Warnings) != 0 {
		t.Fatalf("%v %q", err, c.Warnings)
	}
	if c.GuardOn() || c.WrappersOn() || !c.SkipWrapper("kns") || !c.SkipWrapper("kctx") || c.SkipWrapper("kview") {
		t.Errorf("read %+v", c)
	}
}

// watch is on by default; kview_watch, its old name, still turns it off,
// and watch wins when both are written.
func TestWatchOn(t *testing.T) {
	var none *Config
	if !none.WatchOn() || !(&Config{}).WatchOn() {
		t.Fatal("watch is on by default")
	}
	for _, tc := range []struct {
		yaml string
		on   bool
	}{
		{"watch: false\n", false},
		{"kview_watch: false\n", false},
		{"kview_watch: false\nwatch: true\n", true},
		{"kview_watch: true\nwatch: false\n", false},
	} {
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(tc.yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("REX8S_CONFIG", p)
		c, err := Load()
		if err != nil || len(c.Warnings) != 0 {
			t.Fatalf("%q: %v %q", tc.yaml, err, c.Warnings)
		}
		if c.WatchOn() != tc.on {
			t.Errorf("%q: WatchOn() = %v", tc.yaml, c.WatchOn())
		}
	}
}
