package kube

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/testkube"
)

// fakeKubectl is a Client whose kubectl answers every call with r.
func fakeKubectl(t *testing.T, r testkube.Reply) (Client, *testkube.Kubectl) {
	t.Helper()
	k := testkube.New(t).Default(r)
	return Client{Kubectl: k.Path, Kubeconfig: k.Kubeconfig}, k
}

// A credential plugin that outlives kubectl keeps stdout open: the timeout
// must still hold (B5).
func TestGetTimeoutKillsGrandchild(t *testing.T) {
	cl, _ := fakeKubectl(t, testkube.Reply{Hold: 30 * time.Second, Delay: 30 * time.Second})
	cl.Timeout = time.Second
	start := time.Now()
	_, err := cl.Get("ctx", "get", "pods")
	if took := time.Since(start); took > 8*time.Second { // the group was killed, not just kubectl
		t.Fatalf("Get took %s with a 1s timeout", took)
	}
	if !errors.Is(err, ErrTimeout) || err.Error() != "timeout" {
		t.Fatalf("err = %v, want timeout", err)
	}
}

func TestOutputTimeoutKillsGrandchild(t *testing.T) {
	cl, _ := fakeKubectl(t, testkube.Reply{Hold: 30 * time.Second, Delay: 30 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	_, err := cl.Output(ctx, "ctx", "delete", "pod", "x")
	if took := time.Since(start); took > 8*time.Second {
		t.Fatalf("Output took %s with a 1s timeout", took)
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

// kubectl succeeded but left a process holding the pipe: the output is
// complete, so it is not an error.
func TestGetSucceedsWhenChildHoldsPipe(t *testing.T) {
	cl, _ := fakeKubectl(t, testkube.Reply{Hold: 30 * time.Second, Stdout: `{"items":[]}` + "\n"})
	cl.Timeout = 20 * time.Second
	start := time.Now()
	out, err := cl.Get("ctx", "get", "pods")
	if err != nil || strings.TrimSpace(string(out)) != `{"items":[]}` {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if took := time.Since(start); took > WaitDelay+8*time.Second {
		t.Fatalf("Get took %s", took)
	}
}

// Warnings on stderr must not end up in the JSON (B6); stderr only makes the
// error.
func TestStderrKeptApart(t *testing.T) {
	cl, k := fakeKubectl(t, testkube.Reply{
		Stderr: "I1117 Waited for 1.1s due to client-side throttling\n",
		Stdout: `{"items":[{"metadata":{"name":"a"},"status":{"phase":"Running"}}]}` + "\n",
	})
	k.On("*", "*fail*", testkube.Reply{Stderr: "some detail\nerror: the server doesn't have a resource type\n", Exit: 1})
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
	cl, _ := fakeKubectl(t, testkube.Reply{Shell: `echo "$KUBECONFIG|$*"`})
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

// Get adds --request-timeout from Timeout, after the caller's arguments.
func TestGetRequestTimeout(t *testing.T) {
	cl, k := fakeKubectl(t, testkube.Reply{Stdout: "ok"})
	cl.Timeout = 30 * time.Second
	if _, err := cl.Get("use", "get", "ns"); err != nil {
		t.Fatal(err)
	}
	if got := k.Calls(); len(got) != 1 || got[0] != "--context use get ns --request-timeout=29s" {
		t.Errorf("calls = %q", got)
	}
}

// Gets to one context run one at a time (B13); a lock directory fails the
// call when two overlap.
func TestGetSerializedPerContext(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "lock")
	cl, _ := fakeKubectl(t, testkube.Reply{Stdout: "ok\n", Shell: `mkdir "` + lock + `" 2>/dev/null || { echo overlap >&2; exit 1; }
sleep 0.2
rmdir "` + lock + `"`})
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
	cl, _ := fakeKubectl(t, testkube.Reply{Stdout: "out\n", Stderr: "err\n", Exit: 3})
	out, err := cl.Exec(context.Background(), "ctx", "app", "web-1", "", []string{"true"})
	if err == nil || !strings.Contains(out, "out") || !strings.Contains(out, "err") {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestRunningPods(t *testing.T) {
	cl, _ := fakeKubectl(t, testkube.Reply{Stdout: `{"items":[
	 {"metadata":{"name":"b"},"status":{"phase":"Running"}},
	 {"metadata":{"name":"a"},"status":{"phase":"Running"}},
	 {"metadata":{"name":"gone","deletionTimestamp":"2026-01-01T00:00:00Z"},"status":{"phase":"Running"}},
	 {"metadata":{"name":"wait"},"status":{"phase":"Pending"}}]}`})
	names, err := cl.RunningPods("ctx", "app", "app=web")
	if err != nil || strings.Join(names, ",") != "a,b" {
		t.Fatalf("names=%v err=%v", names, err)
	}
	if _, err := cl.RunningPods("ctx", "app", ""); err == nil {
		t.Error("an empty selector must fail")
	}
}
