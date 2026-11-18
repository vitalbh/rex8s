package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The test binary doubles as rex8s-guard: run with REX8S_GUARD_MAIN=1 it is
// main() with the same arguments.
func TestMain(m *testing.M) {
	if os.Getenv("REX8S_GUARD_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const kubeconfig = `apiVersion: v1
kind: Config
current-context: use
clusters:
- name: use
  cluster: {server: "https://k8s.us-east.example.com"}
- name: stg
  cluster: {server: "https://k8s.staging.example.com"}
users:
- name: me
  user: {token: x}
contexts:
- name: use
  context: {cluster: use, user: me}
- name: stg
  context: {cluster: stg, user: me}
`

const cfg = `clusters:
  - { match: k8s.us-east.example.com, name: use, prod: true }
  - { match: k8s.staging.example.com, name: stg }
`

// runGuard runs the wrapper end to end in demo mode: a fake kubectl (it only
// prints its arguments) first on PATH, a kubeconfig of example.com clusters,
// no terminal on stdin — like an agent or a script.
func runGuard(t *testing.T, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runGuardWith(t, guardRun{}, env, args...)
}

type guardRun struct {
	// shim: run through links as setup installs them, w/kubectl ->
	// rex8s-guard -> this test binary; args[0] is the link. The fake tools
	// stay in bin/, which demo mode puts first on PATH (before Homebrew's:
	// a real kubectl must never run here), so this checks the dispatch, not
	// the search (internal/pathx and internal/guard test that).
	shim  bool
	extra string // appended to config.yaml
}

func runGuardWith(t *testing.T, o guardRun, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	dir := t.TempDir()
	tools := "bin"
	for _, d := range []string{"bin", "w"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake := "#!/bin/sh\necho \"FAKE $(basename \"$0\") $*\"\n"
	for _, f := range []struct{ name, data string }{
		{tools + "/kubectl", fake}, {tools + "/helm", fake}, {tools + "/flux", fake},
		{"kubeconfig", kubeconfig}, {"config.yaml", cfg + o.extra},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if o.shim {
		bin := filepath.Join(dir, "w")
		for _, l := range [][2]string{{exe, "rex8s-guard"}, {exe, "rex8s"}, {"rex8s-guard", "kubectl"}, {"rex8s-guard", "helm"}, {"rex8s-guard", "flux"}} {
			if err := os.Symlink(l[0], filepath.Join(bin, l[1])); err != nil {
				t.Fatal(err)
			}
		}
		exe, args = filepath.Join(bin, args[0]), args[1:]
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append([]string{
		"REX8S_GUARD_MAIN=1",
		"PATH=/usr/bin:/bin",
		"HOME=" + dir,
		"XDG_CONFIG_HOME=" + dir,
		"XDG_CACHE_HOME=" + dir,
		"REX8S_DEMO=" + dir,
		"KUBECONFIG=" + filepath.Join(dir, "kubeconfig"),
	}, env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "rex8s", "audit.log")); err == nil && o.extra != "" {
		stderr = "AUDIT LOG WRITTEN\n"
	}
	return out.String(), errb.String() + stderr, code
}

func TestGuardBinary(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		args string
		ran  bool
	}{
		{"read on production", nil, "kubectl get pods", true},
		{"verbose read on production", nil, "kubectl -v 6 get pods", true},
		{"change on production, no terminal", nil, "kubectl delete pod x", false},
		{"global flag before the verb", nil, "kubectl -v 6 delete pod x", false},
		{"unknown verb on production", nil, "kubectl frobnicate x", false},
		{"helm upgrade on production", nil, "helm upgrade web ./chart", false},
		{"flux reconcile on production", nil, "flux reconcile ks apps", false},
		{"change off production", nil, "kubectl --context stg delete pod x", true},
		{"forced", []string{"REX8S_FORCE=1"}, "kubectl delete pod x", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, errOut, code := runGuard(t, c.env, strings.Fields(c.args)...)
			want := "FAKE " + c.args + "\n"
			if c.ran {
				if code != 0 || out != want {
					t.Fatalf("exit %d, stdout %q (want %q), stderr %q", code, out, want, errOut)
				}
				return
			}
			if code != 1 || out != "" || !strings.Contains(errOut, "refused") || !strings.Contains(errOut, "REX8S_FORCE=1") {
				t.Fatalf("exit %d, stdout %q, stderr %q: want a refusal", code, out, errOut)
			}
		})
	}
}

func TestPrompt(t *testing.T) {
	if out, _, _ := runGuard(t, nil, "--prompt", "plain"); out != "PROD use" {
		t.Errorf("prompt = %q", out)
	}
	if out, _, _ := runGuard(t, nil, "--prompt", "zsh"); out != "%B%F{red}PROD use%f%b" {
		t.Errorf("zsh prompt = %q", out)
	}
}

func TestVersion(t *testing.T) {
	if out, _, code := runGuard(t, nil, "--version"); out != "dev\n" || code != 0 {
		t.Errorf("--version = %q, exit %d", out, code)
	}
}

func TestShimArgs(t *testing.T) {
	for _, c := range []struct {
		argv []string
		tool string
		ok   bool
	}{
		{[]string{"/x/bin/kubectl", "get", "pods"}, "kubectl", true},
		{[]string{"helm", "list"}, "helm", true},
		{[]string{"flux", "--version"}, "flux", true},
		{[]string{"/x/bin/rex8s-guard", "kubectl", "get"}, "", false},
		{[]string{"rex8s-guard.test"}, "", false},
	} {
		tool, args, ok := shimArgs(c.argv)
		if tool != c.tool || ok != c.ok || (ok && strings.Join(args, " ") != strings.Join(c.argv[1:], " ")) {
			t.Errorf("shimArgs(%q) = %q %q %v", c.argv, tool, args, ok)
		}
	}
}

// Run as w/kubectl -> rex8s-guard (a link, as setup installs it), the
// guard is kubectl: same policy, then the real tool (never itself).
func TestGuardAsLink(t *testing.T) {
	for _, c := range []struct {
		args string
		ran  bool
	}{
		{"kubectl get pods", true},
		{"kubectl delete pod x", false},
		{"helm upgrade web ./chart", false},
		{"flux --version", true},
		{"kubectl --context stg delete pod x", true},
	} {
		out, errOut, code := runGuardWith(t, guardRun{shim: true}, nil, strings.Fields(c.args)...)
		if c.ran {
			if want := "FAKE " + c.args + "\n"; code != 0 || out != want {
				t.Errorf("%s: exit %d, stdout %q (want %q), stderr %q", c.args, code, out, want, errOut)
			}
			continue
		}
		if code != 1 || out != "" || !strings.Contains(errOut, "refused") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q: want a refusal", c.args, code, out, errOut)
		}
	}
}

// guard: false: everything goes straight to the tool, nothing logged.
func TestGuardOff(t *testing.T) {
	for _, o := range []guardRun{{extra: "guard: false\n"}, {shim: true, extra: "guard: false\n"}} {
		out, errOut, code := runGuardWith(t, o, nil, "kubectl", "delete", "pod", "x")
		if code != 0 || out != "FAKE kubectl delete pod x\n" || errOut != "" {
			t.Errorf("shim=%v: exit %d, stdout %q, stderr %q", o.shim, code, out, errOut)
		}
	}
}
