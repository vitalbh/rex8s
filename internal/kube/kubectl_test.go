package kube

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeKubectl writes a shell script standing in for kubectl and returns a
// Client that runs it.
func fakeKubectl(t *testing.T, script string) Client {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return Client{Kubectl: bin, Kubeconfig: filepath.Join(dir, "config")}
}

// A credential plugin that outlives kubectl keeps stdout open: the timeout
// must still hold (B5).
func TestGetTimeoutKillsGrandchild(t *testing.T) {
	cl := fakeKubectl(t, "sleep 30 &\nsleep 30\n")
	cl.Timeout = time.Second
	start := time.Now()
	_, err := cl.Get("ctx", "get", "pods")
	if took := time.Since(start); took > 1800*time.Millisecond { // the group was killed, not just kubectl
		t.Fatalf("Get took %s with a 1s timeout", took)
	}
	if !errors.Is(err, ErrTimeout) || err.Error() != "timeout" {
		t.Fatalf("err = %v, want timeout", err)
	}
}

func TestOutputTimeoutKillsGrandchild(t *testing.T) {
	cl := fakeKubectl(t, "sleep 30 &\nsleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := cl.Output(ctx, "ctx", "delete", "pod", "x")
	if took := time.Since(start); took > 1800*time.Millisecond {
		t.Fatalf("Output took %s with a 1s timeout", took)
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

// kubectl succeeded but left a process holding the pipe: the output is
// complete, so it is not an error.
func TestGetSucceedsWhenChildHoldsPipe(t *testing.T) {
	cl := fakeKubectl(t, "sleep 5 &\necho '{\"items\":[]}'\n")
	cl.Timeout = 20 * time.Second
	start := time.Now()
	out, err := cl.Get("ctx", "get", "pods")
	if err != nil || strings.TrimSpace(string(out)) != `{"items":[]}` {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if took := time.Since(start); took > WaitDelay+2*time.Second {
		t.Fatalf("Get took %s", took)
	}
}

// Warnings on stderr must not end up in the JSON (B6); stderr only makes the
// error.
func TestStderrKeptApart(t *testing.T) {
	cl := fakeKubectl(t, `case "$*" in
*fail*) echo "some detail" >&2; echo "error: the server doesn't have a resource type" >&2; exit 1;;
esac
echo "I1117 Waited for 1.1s due to client-side throttling" >&2
echo '{"items":[{"metadata":{"name":"a"},"status":{"phase":"Running"}}]}'
`)
	out, err := cl.Get("ctx", "get", "pods", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if ps, err := ParsePods(out); err != nil || len(ps) != 1 {
		t.Fatalf("Get: pods=%v err=%v", ps, err)
	}
	out, err = cl.Output(context.Background(), "ctx", "get", "pods", "-o", "json")
	if ps, perr := ParsePods(out); err != nil || perr != nil || len(ps) != 1 {
		t.Fatalf("Output: err=%v parse=%v", err, perr)
	}

	if _, err := cl.Get("ctx", "get", "fail"); err == nil || err.Error() != "error: the server doesn't have a resource type" {
		t.Errorf("Get error = %v, want stderr's last line", err)
	}
	if _, err := cl.Output(context.Background(), "ctx", "get", "fail"); err == nil || !strings.Contains(err.Error(), "some detail") {
		t.Errorf("Output error = %v, want stderr", err)
	}
}

func TestCommandArgsAndKubeconfig(t *testing.T) {
	cl := fakeKubectl(t, `echo "$KUBECONFIG|$*"`)
	cl.Kubeconfig = "/a:/b"
	out, err := cl.Output(context.Background(), "use", "get", "ns")
	if err != nil || strings.TrimSpace(string(out)) != "/a:/b|--context use get ns" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	out, err = cl.Output(context.Background(), "", "config", "view")
	if err != nil || strings.TrimSpace(string(out)) != "/a:/b|config view" {
		t.Fatalf("no context: out=%q err=%v", out, err)
	}
}

// Gets to one context run one at a time (B13); a lock directory fails the
// call when two overlap.
func TestGetSerializedPerContext(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "lock")
	cl := fakeKubectl(t, `mkdir "`+lock+`" 2>/dev/null || { echo overlap >&2; exit 1; }
sleep 0.2
rmdir "`+lock+`"
echo ok
`)
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = cl.Get("same", "get", "pods")
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Gets overlapped: %v", err)
		}
	}
}

func TestExecCombinesOutput(t *testing.T) {
	cl := fakeKubectl(t, `echo out; echo err >&2; exit 3`)
	out, err := cl.Exec(context.Background(), "ctx", "app", "web-1", "", []string{"true"})
	if err == nil || !strings.Contains(out, "out") || !strings.Contains(out, "err") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestRunningPods(t *testing.T) {
	cl := fakeKubectl(t, `echo '{"items":[
	 {"metadata":{"name":"b"},"status":{"phase":"Running"}},
	 {"metadata":{"name":"a"},"status":{"phase":"Running"}},
	 {"metadata":{"name":"gone","deletionTimestamp":"2026-01-01T00:00:00Z"},"status":{"phase":"Running"}},
	 {"metadata":{"name":"wait"},"status":{"phase":"Pending"}}]}'`)
	names, err := cl.RunningPods("ctx", "app", "app=web")
	if err != nil || strings.Join(names, ",") != "a,b" {
		t.Fatalf("names=%v err=%v", names, err)
	}
	if _, err := cl.RunningPods("ctx", "app", ""); err == nil {
		t.Error("an empty selector must fail")
	}
}
