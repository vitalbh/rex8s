package main

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vitalbh/rex8s/internal/config"
)

func TestBrewStable(t *testing.T) {
	if got, ok := brewStable("/opt/homebrew/Cellar/rex8s/0.1.0/bin/rex8s", "rex8s"); !ok || got != "/opt/homebrew/opt/rex8s/bin/rex8s" {
		t.Errorf("brewStable = %q %v", got, ok)
	}
	if _, ok := brewStable("/Users/x/.config/rex/rex8s/bin/rex8s", "rex8s"); ok {
		t.Error("not a brew path")
	}
}

// Without Rex, setup installs the commands and leaves Rex's config alone.
func TestSetupWithoutRex(t *testing.T) {
	dir, a := setupEnv(t, &config.Config{})
	if err := a.setup(nil); err != nil {
		t.Fatal(err)
	}
	home := os.Getenv("HOME")
	if !exists(filepath.Join(dir, "kview")) {
		t.Error("the commands should be installed")
	}
	rexDir := filepath.Join(home, ".config", "rex")
	for _, f := range []string{"init.lua", "rex8s.lua"} {
		if _, err := os.Stat(filepath.Join(rexDir, f)); err == nil {
			t.Errorf("%s written without Rex", f)
		}
	}
}

// setupEnv: a throwaway HOME, no Rex, no terminal, and a binDir with
// stand-ins for both binaries. It returns the wrapper dir and an app for
// setup. askGuard fails the test unless a test replaces it.
func setupEnv(t *testing.T, cfg *config.Config) (string, *app) {
	t.Helper()
	home := t.TempDir()
	for k, v := range map[string]string{"HOME": home, "XDG_CONFIG_HOME": "", "XDG_CACHE_HOME": "", "REX_CONFIG_DIR": "", "REX_CONFIG": "", "REX8S_CONFIG": ""} {
		t.Setenv(k, v)
	}
	oldRex, oldTTY, oldAsk := findRex, stdinTTY, askGuard
	findRex = func() string { return "" }
	stdinTTY = func() bool { return false }
	askGuard = func() (bool, bool) { t.Error("setup asked about the guard"); return true, true }
	t.Cleanup(func() { findRex, stdinTTY, askGuard = oldRex, oldTTY, oldAsk })

	src := t.TempDir()
	for _, n := range []string{"rex8s", "rex8s-guard"} {
		if err := os.WriteFile(filepath.Join(src, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if cfg.AskAgent == "" {
		cfg.AskAgent = "claude"
	}
	return filepath.Join(home, ".config", "rex", "rex8s", "bin"), &app{cfg: cfg, binDir: src}
}

// links: name -> target of every link in dir.
func links(t *testing.T, dir string) map[string]string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range es {
		if tg, err := os.Readlink(filepath.Join(dir, e.Name())); err == nil {
			out[e.Name()] = tg
		}
	}
	return out
}

// setup links each command to its binary, replacing the sh scripts older
// versions wrote; uninstall removes them all.
func TestSetupLinks(t *testing.T) {
	dir, a := setupEnv(t, &config.Config{})
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := map[string]string{
		"kview":   "#!/bin/sh\n# rex8s wrapper\nexec \"$(dirname \"$0\")/rex8s\" kview \"$@\"\n",
		"kubectl": "#!/bin/sh\n# rex8s wrapper\nexec \"$(dirname \"$0\")/rex8s-guard\" kubectl \"$@\"\n",
	}
	for n, data := range old {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.setup(nil); err != nil {
		t.Fatal(err)
	}
	got := links(t, dir)
	for _, n := range wrapperNames() {
		want := "rex8s"
		if slices.Contains(guardedTools, n) {
			want = "rex8s-guard"
		}
		if got[n] != want {
			t.Errorf("%s -> %q, want %s", n, got[n], want)
		}
	}
	if len(got) != len(wrapperNames()) {
		t.Errorf("%d links, want %d: %v", len(got), len(wrapperNames()), got)
	}
	if st := inspectLinks(dir, a.cfg); st.missing+st.stale+len(st.extra) != 0 {
		t.Errorf("doctor would complain: %+v", st)
	}
	// again: nothing to change
	if st, err := linkWrappers(dir, a.cfg); err != nil || st.made+st.removed != 0 {
		t.Errorf("second run changed %d, removed %d (%v)", st.made, st.removed, err)
	}

	if err := a.uninstall(nil); err != nil {
		t.Fatal(err)
	}
	if es, _ := os.ReadDir(dir); len(es) != 0 {
		t.Errorf("left after uninstall: %v", es)
	}
}

// skip_wrappers, wrappers: false and guard: false, each applied by running
// setup again.
func TestSetupWrapperOptions(t *testing.T) {
	no := false
	dir, a := setupEnv(t, &config.Config{})
	if err := a.setup(nil); err != nil {
		t.Fatal(err)
	}

	a.cfg.SkipWrappers = []string{"kns", "kctx", "nope"}
	if err := a.setup(nil); err != nil {
		t.Fatal(err)
	}
	got := links(t, dir)
	if _, ok := got["kns"]; ok {
		t.Error("kns installed despite skip_wrappers")
	}
	if _, ok := got["kctx"]; ok {
		t.Error("kctx installed despite skip_wrappers")
	}
	if got["kview"] != "rex8s" || got["kubectl"] != "rex8s-guard" {
		t.Errorf("the others should stay: %v", got)
	}
	if st := inspectLinks(dir, a.cfg); !slices.Equal(st.unknownSkip, []string{"nope"}) {
		t.Errorf("unknown skip_wrappers: %q", st.unknownSkip)
	}

	a.cfg.SkipWrappers, a.cfg.Wrappers = nil, &no
	if err := a.setup(nil); err != nil {
		t.Fatal(err)
	}
	got = links(t, dir)
	for n := range got {
		if strings.HasPrefix(n, "k") && n != "kubectl" {
			t.Errorf("wrappers: false left %s", n)
		}
	}
	if got["kubectl"] != "rex8s-guard" || got["helm"] != "rex8s-guard" || got["flux"] != "rex8s-guard" {
		t.Errorf("wrappers: false keeps the guard: %v", got)
	}

	a.cfg.Guard = &no
	if err := a.setup(nil); err != nil {
		t.Fatal(err)
	}
	if got = links(t, dir); len(got) != 0 {
		t.Errorf("wrappers and guard off: links left %v", got)
	}
	for _, n := range []string{"rex8s", "rex8s-guard"} {
		if !exists(filepath.Join(dir, n)) {
			t.Errorf("%s removed: rex8s kview and rex8s-guard kubectl still need it", n)
		}
	}

	a.cfg.Wrappers = nil
	if err := a.setup(nil); err != nil {
		t.Fatal(err)
	}
	if got = links(t, dir); got["kview"] != "rex8s" || got["kubectl"] != "" {
		t.Errorf("guard: false only: %v", got)
	}
}

// The guard question: never without a terminal, never once guard is set;
// at a terminal the first time, the answer saved and applied.
func TestSetupAsksAboutTheGuard(t *testing.T) {
	dir, a := setupEnv(t, &config.Config{}) // no terminal: askGuard fails the test
	if err := a.setup(nil); err != nil {
		t.Fatal(err)
	}
	if exists(config.Path()) {
		t.Error("config.yaml written without a terminal")
	}
	if links(t, dir)["kubectl"] != "rex8s-guard" {
		t.Error("without a terminal the guard is installed")
	}

	yes := true
	a.cfg.Guard = &yes
	stdinTTY = func() bool { return true }
	if err := a.setup(nil); err != nil { // set: not asked
		t.Fatal(err)
	}

	a.cfg = &config.Config{AskAgent: "claude"}
	asked := 0
	askGuard = func() (bool, bool) { asked++; return false, true }
	if err := a.setup(nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(config.Path())
	if asked != 1 || err != nil || !strings.Contains(string(data), "guard: false") {
		t.Fatalf("asked %d times, config.yaml %q (%v)", asked, data, err)
	}
	if _, ok := links(t, dir)["kubectl"]; ok {
		t.Error("answered no: kubectl still linked")
	}
	cfg, err := config.Load()
	if err != nil || cfg.GuardOn() {
		t.Fatalf("saved config: guard on=%v (%v)", cfg.GuardOn(), err)
	}
	a.cfg = cfg
	if err := a.setup(nil); err != nil || asked != 1 {
		t.Fatalf("asked again (%d): %v", asked, err)
	}
}

// A Homebrew install: rex8s found through a link into the Cellar (as
// /opt/homebrew/bin/rex8s is). The wrapper dir gets a link to the opt path,
// which upgrades keep, and the k* links point at that link.
func TestSetupBrew(t *testing.T) {
	dir, a := setupEnv(t, &config.Config{})
	root, err := filepath.EvalSymlinks(t.TempDir()) // macOS: /var is a link
	if err != nil {
		t.Fatal(err)
	}
	cellar := filepath.Join(root, "Cellar", "rex8s", "1.0", "bin")
	brewBin := filepath.Join(root, "bin")
	for _, d := range []string{cellar, brewBin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"rex8s", "rex8s-guard"} {
		if err := os.WriteFile(filepath.Join(cellar, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(cellar, n), filepath.Join(brewBin, n)); err != nil {
			t.Fatal(err)
		}
	}
	a.binDir = brewBin
	if err := a.setup(nil); err != nil {
		t.Fatal(err)
	}
	got := links(t, dir)
	if want := filepath.Join(root, "opt", "rex8s", "bin", "rex8s"); got["rex8s"] != want {
		t.Errorf("rex8s -> %q, want %q", got["rex8s"], want)
	}
	if got["kview"] != "rex8s" || got["kubectl"] != "rex8s-guard" {
		t.Errorf("links %v", got)
	}
}

func TestCommandArgs(t *testing.T) {
	for _, c := range []struct{ argv, want []string }{
		{[]string{"/x/bin/klogs", "-f"}, []string{"logs", "-f"}},
		{[]string{"kview"}, []string{"kview"}},
		{[]string{"/x/bin/rex8s", "logs", "-f"}, []string{"logs", "-f"}},
		{[]string{"/x/bin/rex8s", "klogs"}, []string{"klogs"}}, // old wrapper scripts: rex8s CMD
		{[]string{"/x/bin/rex8s"}, []string{}},
		{[]string{"rex8s-dev", "version"}, []string{"version"}},
	} {
		if got := commandArgs(c.argv); !slices.Equal(got, c.want) {
			t.Errorf("commandArgs(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}

// doctor checks the links against the config and says when the guard is off.
func TestDoctorInstall(t *testing.T) {
	no := false
	dir, a := setupEnv(t, &config.Config{Guard: &no})
	if err := a.setup(nil); err != nil {
		t.Fatal(err)
	}
	doctor := func() (string, int) {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stdout
		os.Stdout = w
		d := &checker{}
		a.doctorInstall(d)
		os.Stdout = old
		w.Close()
		out, _ := io.ReadAll(r)
		return string(out), d.bad
	}
	out, _ := doctor()
	if !strings.Contains(out, "guard: off") {
		t.Errorf("no guard: off in:\n%s", out)
	}
	if strings.Contains(out, "missing") {
		t.Errorf("links reported missing right after setup:\n%s", out)
	}
	// an old script left behind is outdated
	if err := os.Remove(filepath.Join(dir, "kview")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kview"), []byte("#!/bin/sh\n# rex8s wrapper\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, _ := doctor(); !strings.Contains(out, "0 missing, 1 outdated, 0 to remove") {
		t.Errorf("old script not reported:\n%s", out)
	}
}
