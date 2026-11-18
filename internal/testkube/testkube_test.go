package testkube

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, k *Kubectl, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(k.Path, args...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return o.String(), e.String(), code
}

func TestMatchByContextAndArgs(t *testing.T) {
	k := New(t)
	k.On("use", "get pods *", Reply{Stdout: `{"items":[]}`})
	k.On("euw*", "*", Reply{Stderr: "error: Unauthorized\n", Exit: 1})
	k.On("", "config view", Reply{Stdout: "it's 'quoted' $HOME\n"})

	if out, _, code := run(t, k, "--context", "use", "get", "pods", "-A"); out != `{"items":[]}` || code != 0 {
		t.Errorf("use: %q %d", out, code)
	}
	if _, errOut, code := run(t, k, "--context", "euw-2", "get", "nodes"); errOut != "error: Unauthorized\n" || code != 1 {
		t.Errorf("euw: %q %d", errOut, code)
	}
	if out, _, _ := run(t, k, "config", "view"); out != "it's 'quoted' $HOME\n" {
		t.Errorf("no context: %q", out)
	}
	// "use" with other arguments, and a context matching nothing
	for _, args := range [][]string{{"--context", "use", "get", "nodes"}, {"--context", "sa", "get", "pods", "-A"}} {
		if _, errOut, code := run(t, k, args...); code != 1 || !strings.Contains(errOut, "no answer for "+strings.Join(args, " ")) {
			t.Errorf("%v: %q %d", args, errOut, code)
		}
	}
	if got := k.Calls(); len(got) != 5 || got[0] != "--context use get pods -A" || got[2] != "config view" {
		t.Errorf("calls = %q", got)
	}
	if k.Count("--context use") != 2 || k.Count("") != 5 {
		t.Errorf("counts: %d %d", k.Count("--context use"), k.Count(""))
	}
}

func TestReplace(t *testing.T) {
	k := New(t).Default(Reply{Stdout: "default"})
	k.On("*", "get pods", Reply{Stdout: "one"})
	k.On("*", "get pods", Reply{Stdout: "two"}) // same patterns: replaced
	if out, _, _ := run(t, k, "get", "pods"); out != "two" {
		t.Errorf("got %q", out)
	}
	if out, _, _ := run(t, k, "get", "svc"); out != "default" {
		t.Errorf("default: %q", out)
	}
}

func TestShellSeesArgs(t *testing.T) {
	k := New(t).Default(Reply{Shell: `echo "$# $*"; echo "$D"`})
	out, _, _ := run(t, k, "--context", "use", "get", "ns")
	if out != "4 --context use get ns\n"+k.Dir+"\n" {
		t.Errorf("out = %q", out)
	}
}

func TestTiming(t *testing.T) {
	k := New(t).Default(Reply{Stdout: "x", Delay: 300 * time.Millisecond, After: 300 * time.Millisecond, Exit: 3})
	start := time.Now()
	_, _, code := run(t, k, "get")
	if took := time.Since(start); took < 600*time.Millisecond || code != 3 {
		t.Errorf("took %s, exit %d", took, code)
	}

	// Hold: the fake exits at once, its child keeps the pipe open
	k = New(t).Default(Reply{Stdout: "x", Hold: 5 * time.Second})
	cmd := exec.Command(k.Path)
	cmd.WaitDelay = 100 * time.Millisecond
	var o bytes.Buffer
	cmd.Stdout = &o
	start = time.Now()
	err := cmd.Run()
	if !errors.Is(err, exec.ErrWaitDelay) || o.String() != "x" || time.Since(start) > 4*time.Second {
		t.Errorf("hold: err=%v out=%q after %s", err, o.String(), time.Since(start))
	}
}
