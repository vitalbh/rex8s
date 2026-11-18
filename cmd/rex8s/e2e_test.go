package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The end-to-end tests build rex8s and run it in demo mode: demo/kubeconfig
// (example.com clusters) answered by demo/bin/kubectl, with a throwaway
// HOME, so nothing reads or writes the real config, cache or clusters.

const e2eVersion = "v0.0.0-e2e"

// e2eEnv is the environment for a demo run: no KUBECONFIG, REX_* or proxy
// from the caller, and a PATH with python3 (the demo kubectl) and the base
// tools only; rex8s puts demo/bin first itself.
func e2eEnv(t *testing.T, home, demoDir string) []string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found: the demo kubectl needs it")
	}
	tmp := filepath.Join(home, "tmp") // the demo kubectl keeps its state under TMPDIR
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{
		"PATH=" + filepath.Dir(py) + ":/usr/bin:/bin",
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"TMPDIR=" + tmp,
		"REX8S_DEMO=" + demoDir,
		"TERM=dumb",
		"NO_COLOR=1",
		"LANG=C.UTF-8",
	}
}

// buildRex8s builds this package with a version, as the release does.
func buildRex8s(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "rex8s")
	// go test puts its own go first on PATH
	cmd := exec.Command("go", "build", "-ldflags", "-X main.version="+e2eVersion, "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func TestEndToEndDemo(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the demo kubectl is a script")
	}
	demoDir, err := filepath.Abs(filepath.Join("..", "..", "demo"))
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	env := e2eEnv(t, home, demoDir)
	bin := buildRex8s(t)

	runAs := func(t *testing.T, exe string, args ...string) (stdout, stderr string) {
		t.Helper()
		cmd := exec.Command(exe, args...)
		cmd.Env, cmd.Dir = env, home
		var o, e bytes.Buffer
		cmd.Stdout, cmd.Stderr = &o, &e
		done := make(chan error, 1)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("rex8s %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, o.String(), e.String())
			}
		case <-time.After(60 * time.Second):
			cmd.Process.Kill()
			t.Fatalf("rex8s %s did not finish", strings.Join(args, " "))
		}
		return o.String(), e.String()
	}
	run := func(t *testing.T, args ...string) (stdout, stderr string) {
		t.Helper()
		return runAs(t, bin, args...)
	}
	contains := func(t *testing.T, what, got string, wants ...string) {
		t.Helper()
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Errorf("%s: no %q in:\n%s", what, w, got)
			}
		}
	}
	decode := func(t *testing.T, what, out string, v any) {
		t.Helper()
		if err := json.Unmarshal([]byte(out), v); err != nil {
			t.Fatalf("%s: not JSON (%v):\n%s", what, err, out)
		}
	}

	t.Run("version", func(t *testing.T) {
		out, _ := run(t, "version")
		if strings.TrimSpace(out) != e2eVersion {
			t.Errorf("version = %q, want %q", out, e2eVersion)
		}
	})

	t.Run("list clusters", func(t *testing.T) {
		out, _ := run(t, "list", "clusters")
		var cs []clusterJSON
		decode(t, "clusters", out, &cs)
		short := map[string]clusterJSON{}
		for _, c := range cs {
			short[c.Short] = c
		}
		for _, s := range []string{"use", "euw", "staging", "sa"} {
			if _, ok := short[s]; !ok {
				t.Errorf("no cluster %q in %+v", s, cs)
			}
		}
		if !short["use"].Prod || short["staging"].Prod || !short["use"].Current {
			t.Errorf("prod/current flags: %+v", cs)
		}
		if !strings.HasSuffix(strings.TrimSuffix(short["euw"].Server, ":6443"), ".example.com") {
			t.Errorf("not the demo kubeconfig: %+v", short["euw"])
		}
	})

	t.Run("list pods", func(t *testing.T) {
		out, _ := run(t, "list", "pods", "acme-prod-use")
		var ps []podJSON
		decode(t, "pods", out, &ps)
		failing := 0
		for _, p := range ps {
			if p.Namespace == "" || p.Name == "" || p.Total == 0 {
				t.Errorf("incomplete pod %+v", p)
			}
			if p.Badge == "FAIL" {
				failing++
			}
		}
		if len(ps) < 5 || failing == 0 {
			t.Errorf("%d pods, %d failing: %s", len(ps), failing, out)
		}
	})

	t.Run("list failing", func(t *testing.T) {
		out, _ := run(t, "list", "failing")
		var cs []struct {
			Short        string    `json:"short"`
			LoginExpired bool      `json:"loginExpired"`
			Pods         []podJSON `json:"pods"`
		}
		decode(t, "failing", out, &cs)
		if len(cs) != 4 {
			t.Fatalf("%d clusters: %s", len(cs), out)
		}
		failing := 0
		for _, c := range cs {
			if c.LoginExpired != (c.Short == "sa") { // the demo's sa has an expired login
				t.Errorf("%s: loginExpired = %v", c.Short, c.LoginExpired)
			}
			failing += len(c.Pods)
		}
		if failing == 0 {
			t.Errorf("no failing pods: %s", out)
		}
	})

	t.Run("list workloads", func(t *testing.T) {
		out, _ := run(t, "list", "workloads")
		var ws []struct {
			Short string `json:"short"`
			Kind  string `json:"kind"`
			Name  string `json:"name"`
			Tag   string `json:"tag"`
		}
		decode(t, "workloads", out, &ws)
		found := false
		for _, w := range ws {
			if w.Kind == "" || w.Name == "" {
				t.Errorf("incomplete workload %+v", w)
			}
			found = found || (w.Short == "staging" && w.Name == "api" && w.Tag != "")
		}
		if !found {
			t.Errorf("no api on staging: %s", out)
		}
	})

	t.Run("kver", func(t *testing.T) {
		out, errOut := run(t, "kver")
		contains(t, "kver", out, "workload", "euw", "staging", "use", "api deploy", "1.42.0")
		contains(t, "kver stderr", errOut, "reading workloads on 4 clusters")
	})

	// kver -> rex8s, as setup links it: the name is the command
	t.Run("as a link", func(t *testing.T) {
		w := t.TempDir()
		for _, l := range [][2]string{{bin, "rex8s"}, {bin, "rex8s-guard"}, {"rex8s", "kver"}} {
			if err := os.Symlink(l[0], filepath.Join(w, l[1])); err != nil {
				t.Fatal(err)
			}
		}
		out, _ := runAs(t, filepath.Join(w, "kver"))
		contains(t, "kver as a link", out, "workload", "euw", "api deploy", "1.42.0")
	})

	t.Run("kstatus", func(t *testing.T) {
		out, _ := run(t, "kstatus", "--context", "euw")
		contains(t, "kstatus", out, "euw", "acme-prod-euw", "Nodes", "Pods", "Workloads", "Flux")
	})
}
