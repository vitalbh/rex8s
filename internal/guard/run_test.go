package guard

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/kube"
)

const testKubeconfig = `apiVersion: v1
kind: Config
current-context: stg
clusters:
- name: use-cluster
  cluster: {server: "https://k8s.us-east.example.com"}
- name: euw-cluster
  cluster: {server: "https://k8s.eu-west.example.com"}
- name: stg-cluster
  cluster: {server: "https://k8s.staging.example.com"}
users:
- name: use-admin
  user: {token: x}
- name: euw-admin
  user: {token: x}
- name: stg-user
  user: {token: x}
contexts:
- name: use
  context: {cluster: use-cluster, user: use-admin}
- name: euw
  context: {cluster: euw-cluster, user: euw-admin}
- name: stg
  context: {cluster: stg-cluster, user: stg-user}
- name: use-all
  context: {cluster: use-cluster, user: use-admin, namespace: "*"}
`

var testCfg = &config.Config{Clusters: []config.Cluster{
	{Match: "k8s.us-east.example.com", Name: "use", Prod: true},
	{Match: "k8s.eu-west.example.com", Name: "euw", Prod: true},
	{Match: "k8s.staging.example.com", Name: "stg"},
}}

// fake is the outside world for one test: environment, terminal, the answer
// typed at the prompt, and the command that would have been exec'd.
type fake struct {
	env    map[string]string
	tty    bool   // stdin is a terminal
	answer string // typed at the prompt; "" with noTTY: /dev/tty can't be opened
	noTTY  bool
	asked  bool
	stderr bytes.Buffer
	ran    []string
	bin    string // what ran
}

func setup(t *testing.T, f *fake) string {
	t.Helper()
	dir := t.TempDir()
	kc := filepath.Join(dir, "config")
	if err := os.WriteFile(kc, []byte(testKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kc)
	if f.env == nil {
		f.env = map[string]string{}
	}
	f.env["XDG_CACHE_HOME"] = dir
	old, oldNow := sys, now
	sys = system{
		getenv:   func(k string) string { return f.env[k] },
		stdinTTY: func() bool { return f.tty },
		openTTY: func() (io.ReadCloser, error) {
			f.asked = true
			if f.noTTY {
				return nil, errors.New("no tty")
			}
			return io.NopCloser(strings.NewReader(f.answer + "\n")), nil
		},
		stderr: &f.stderr,
		color:  func() bool { return false },
		exec: func(bin string, argv []string) error {
			f.ran, f.bin = argv, bin
			return nil
		},
	}
	now = func() time.Time { return time.Date(2026, 11, 17, 10, 4, 5, 0, time.UTC) }
	t.Cleanup(func() { sys, now = old, oldNow })
	return filepath.Join(dir, "rex8s", "audit.log")
}

func auditLines(t *testing.T, path string) []string {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestRun(t *testing.T) {
	agent := map[string]string{"REX8S_AGENT": "1"}
	cases := []struct {
		name   string
		cmd    string
		env    map[string]string
		tty    bool
		answer string
		ran    bool   // the tool ran
		err    error  // nil, ErrRefused, ErrCancelled, errExit
		audit  string // "outcome\tclusters" of the one audit line; "" none
		ask    bool   // the prompt was shown
	}{
		// FORCE first: no prompt, no refusal, even for an agent
		{"force", "kubectl --context use delete pod x", map[string]string{"REX8S_FORCE": "1", "REX8S_AGENT": "1"}, false, "", true, nil, "forced\tuse", false},
		// then nobody at a terminal: refused
		{"agent", "kubectl --context use delete pod x", agent, true, "use", false, ErrRefused, "refused\tuse", false},
		{"claude code", "kubectl --context use delete pod x", map[string]string{"CLAUDECODE": "1"}, true, "use", false, ErrRefused, "refused\tuse", false},
		{"no tty", "kubectl --context use delete pod x", nil, false, "use", false, ErrRefused, "refused\tuse", false},
		// then the prompt
		{"confirmed", "kubectl --context use delete pod x", nil, true, "use", true, nil, "confirmed\tuse", true},
		{"wrong name", "kubectl --context use delete pod x", nil, true, "y", false, ErrCancelled, "cancelled\tuse", true},
		{"global flag before the verb", "kubectl --context use -v 6 delete pod x", nil, true, "nope", false, ErrCancelled, "cancelled\tuse", true},
		{"unknown verb on prod", "kubectl --context use frobnicate x", nil, true, "use", true, nil, "confirmed\tuse", true},
		{"exec on prod", "kubectl --context use exec -it web-1 -- sh", agent, true, "", false, ErrRefused, "refused\tuse", false},
		// nothing to ask
		{"read on prod, agent", "kubectl --context use get pods", agent, false, "", true, nil, "", false},
		{"change off prod, agent", "kubectl delete pod x", agent, false, "", true, nil, "", false},
		{"verbose read on prod", "kubectl --context use -v 6 get pods", nil, true, "", true, nil, "", false},
		// production found other ways than --context
		{"--cluster", "kubectl --cluster use-cluster delete pod x", agent, true, "", false, ErrRefused, "refused\tuse", false},
		{"--server", "kubectl -s https://k8s.eu-west.example.com delete pod x", agent, true, "", false, ErrRefused, "refused\teuw", false},
		{"--user", "kubectl --user use-admin delete pod x", agent, true, "", false, ErrRefused, "refused\tuse", false},
		{"HELM_KUBECONTEXT", "helm upgrade web ./chart", map[string]string{"HELM_KUBECONTEXT": "use", "REX8S_AGENT": "1"}, true, "", false, ErrRefused, "refused\tuse", false},
		{"HELM_KUBEAPISERVER", "helm upgrade web ./chart", map[string]string{"HELM_KUBEAPISERVER": "https://k8s.us-east.example.com", "REX8S_AGENT": "1"}, true, "", false, ErrRefused, "refused\tuse", false},
		{"--server to staging", "kubectl --context use --server https://k8s.staging.example.com delete pod x", agent, false, "", true, nil, "", false},
		// a pane showing every namespace
		{"all namespaces: one object", "kubectl --context use-all logs web-1", nil, true, "", false, errExit, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{env: c.env, tty: c.tty, answer: c.answer}
			log := setup(t, f)
			args := strings.Fields(c.cmd)
			err := run(args[0], "/fake/"+args[0], args[1:], testCfg)
			if !errors.Is(err, c.err) || (c.err == nil && err != nil) {
				t.Fatalf("err = %v, want %v\nstderr: %s", err, c.err, f.stderr.String())
			}
			if (f.ran != nil) != c.ran {
				t.Errorf("ran = %q, want ran=%v", f.ran, c.ran)
			}
			if f.asked != c.ask {
				t.Errorf("asked = %v, want %v\nstderr: %s", f.asked, c.ask, f.stderr.String())
			}
			lines := auditLines(t, log)
			switch {
			case c.audit == "" && len(lines) != 0:
				t.Errorf("audit = %q, want none", lines)
			case c.audit != "" && (len(lines) != 1 || !strings.Contains(lines[0], "\t"+c.audit+"\t"+c.cmd)):
				t.Errorf("audit = %q, want %q", lines, c.audit+"\t"+c.cmd)
			}
		})
	}
}

func TestRunMessages(t *testing.T) {
	f := &fake{env: map[string]string{"REX8S_AGENT": "1"}, tty: true}
	log := setup(t, f)
	err := run("kubectl", "/fake/kubectl", []string{"--context", "use", "delete", "pod", "x"}, testCfg)
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "REX8S_FORCE=1") || !strings.Contains(err.Error(), "PRODUCTION cluster use") {
		t.Fatalf("refusal: %v", err)
	}
	if l := auditLines(t, log); len(l) != 1 || !strings.HasSuffix(l[0], "(agent)") {
		t.Fatalf("audit should mark the agent: %q", l)
	}

	f = &fake{tty: true, answer: "use"}
	setup(t, f)
	if err := run("kubectl", "/fake/kubectl", []string{"--context", "use", "patch", "deploy", "web", "-p", "{}"}, testCfg); err != nil {
		t.Fatal(err)
	}
	out := f.stderr.String()
	if !strings.Contains(out, " PRODUCTION (use)  kubectl --context use patch") || !strings.Contains(out, "reverted on the next reconcile") ||
		!strings.Contains(out, "type 'use' to confirm") {
		t.Fatalf("prompt:\n%s", out)
	}

	// every namespace: reads get -A, the argv reaches the tool unchanged otherwise
	f = &fake{tty: true}
	setup(t, f)
	if err := run("kubectl", "/fake/kubectl", []string{"--context", "use-all", "get", "pods"}, testCfg); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.ran, " "); got != "kubectl --context use-all get pods -A" {
		t.Fatalf("ran %q", got)
	}
}

func TestConfirmChange(t *testing.T) {
	use := kube.Context{Name: "use", Short: "use", Prod: true}
	euw := kube.Context{Name: "euw", Short: "euw", Prod: true}
	stg := kube.Context{Name: "stg", Short: "stg"}
	cases := []struct {
		name     string
		clusters []kube.Context
		env      map[string]string
		tty      bool
		noTTY    bool
		answer   string
		err      error
		audit    string
	}{
		{"no production: nothing asked, even for an agent", []kube.Context{stg, stg}, map[string]string{"REX8S_AGENT": "1"}, false, false, "", nil, ""},
		{"force", []kube.Context{use, stg}, map[string]string{"REX8S_FORCE": "1", "CLAUDECODE": "1"}, false, false, "", nil, "forced\tuse"},
		{"agent", []kube.Context{use}, map[string]string{"REX8S_AGENT": "1"}, true, false, "use", ErrRefused, "refused\tuse"},
		{"no tty", []kube.Context{stg, use}, nil, false, false, "use", ErrRefused, "refused\tuse"},
		{"typed", []kube.Context{use, stg}, nil, true, false, "use", nil, "confirmed\tuse"},
		{"y is not enough", []kube.Context{use}, nil, true, false, "y", ErrCancelled, "cancelled\tuse"},
		{"two clusters, both typed", []kube.Context{use, euw, use}, nil, true, false, "use, euw", nil, "confirmed\teuw,use"},
		{"two clusters, one typed", []kube.Context{use, euw}, nil, true, false, "use", ErrCancelled, "cancelled\teuw,use"},
		{"no /dev/tty", []kube.Context{use}, nil, true, true, "", ErrCancelled, "cancelled\tuse"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{env: c.env, tty: c.tty, noTTY: c.noTTY, answer: c.answer}
			log := setup(t, f)
			err := ConfirmChange(nil, c.clusters, "kroll: rollout restart 2 workload(s)")
			if !errors.Is(err, c.err) || (c.err == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			lines := auditLines(t, log)
			switch {
			case c.audit == "" && len(lines) != 0:
				t.Errorf("audit = %q, want none", lines)
			case c.audit != "" && (len(lines) != 1 || !strings.Contains(lines[0], "\t"+c.audit+"\tkroll: rollout restart")):
				t.Errorf("audit = %q, want %q", lines, c.audit)
			}
		})
	}
}

func TestAuditRotates(t *testing.T) {
	log := setup(t, &fake{})
	if err := os.MkdirAll(filepath.Dir(log), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, bytes.Repeat([]byte("x"), auditMax), 0o600); err != nil {
		t.Fatal(err)
	}
	audit("use", "confirmed", "kubectl delete pod\tx\ny")
	lines := auditLines(t, log)
	if len(lines) != 1 || lines[0] != "2026-11-17T10:04:05Z\tconfirmed\tuse\tkubectl delete pod x y" {
		t.Fatalf("after rotation: %q", lines)
	}
	if fi, err := os.Stat(log + ".1"); err != nil || fi.Size() != auditMax {
		t.Fatalf("old log: %v %v", fi, err)
	}
}

// guard: false turns the guard off: Run hands any command to the tool and
// ConfirmChange lets any change through, nothing asked, nothing logged.
func TestGuardOff(t *testing.T) {
	off := false
	cfg := *testCfg
	cfg.Guard = &off

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	f := &fake{env: map[string]string{"REX8S_AGENT": "1"}}
	log := setup(t, f)
	args := strings.Fields("--context use delete pod x")
	if err := Run("kubectl", args, "", &cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if want := append([]string{"kubectl"}, args...); strings.Join(f.ran, " ") != strings.Join(want, " ") || f.asked {
		t.Fatalf("ran %q, asked %v", f.ran, f.asked)
	}
	use := kube.Context{Name: "use", Short: "use", Prod: true}
	if err := ConfirmChange(&cfg, []kube.Context{use}, "kroll"); err != nil || f.asked {
		t.Fatalf("ConfirmChange: %v, asked %v", err, f.asked)
	}
	if lines := auditLines(t, log); len(lines) != 0 {
		t.Errorf("audit with the guard off: %q", lines)
	}

	// on (the default): the same change is refused for the agent
	if err := ConfirmChange(testCfg, []kube.Context{use}, "kroll"); !errors.Is(err, ErrRefused) {
		t.Fatalf("guard on: %v", err)
	}
}

// The kubectl the guard runs is never the guard: with kubectl ->
// rex8s-guard first on PATH (setup's link), told to skip that directory or
// not (a Homebrew rex8s-guard resolves into the Cellar), it runs the next one.
func TestRunSkipsItsOwnLink(t *testing.T) {
	root := t.TempDir()
	wrap, real := filepath.Join(root, "wrap"), filepath.Join(root, "real")
	for _, d := range []string{wrap, real} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(wrap, "rex8s-guard"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("rex8s-guard", filepath.Join(wrap, "kubectl")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "kubectl"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrap+string(os.PathListSeparator)+real)
	for _, skip := range []string{wrap, ""} {
		f := &fake{}
		setup(t, f)
		if err := Run("kubectl", []string{"get", "pods"}, skip, testCfg); err != nil {
			t.Fatal(err)
		}
		if f.bin != filepath.Join(real, "kubectl") {
			t.Errorf("skip %q: ran %q, want the real kubectl", skip, f.bin)
		}
	}
}
