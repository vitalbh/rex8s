package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Client runs kubectl against a kubeconfig. Every way of running it goes
// through Command, so they all share the same rules:
//
//   - KUBECONFIG is set to Kubeconfig (not --kubeconfig: it may be a list,
//     a:b, like KUBECONFIG);
//   - "--context NAME" comes first when a context is given;
//   - with a context.Context that can end (WithTimeout, WithCancel), kubectl
//     runs in its own process group and the whole group is killed when it
//     ends, so a credential plugin (oidc-login, aws, gke-gcloud-auth-plugin)
//     that kubectl started cannot keep the call alive; pipes still held open
//     after kubectl exits are closed WaitDelay later.
//
// Get serializes calls per context inside the process: two kubectl calls that
// refresh an OIDC token at the same time can invalidate the refresh token.
type Client struct {
	Kubectl    string
	Kubeconfig string
	Timeout    time.Duration // per Get call; 0 means 10s
}

// ErrTimeout is wrapped by the errors of calls that ran out of time.
var ErrTimeout = errors.New("timeout")

// WaitDelay bounds how long a kubectl's output pipes may stay open after it
// exits or is cancelled (a child process can inherit them).
const WaitDelay = 2 * time.Second

// Command returns `kubectl [--context ctxName] args…` with KUBECONFIG set,
// ready for the caller to wire up and start (streams: logs -f, get -w).
//
// With a ctx that can end, the command gets its own process group, and
// cancelling ctx kills the group. With context.Background() it stays in the
// terminal's process group: use that for commands that read the terminal
// (exec -it) or must stop with the user's ctrl+c.
func (k Client) Command(ctx context.Context, ctxName string, args ...string) *exec.Cmd {
	full := make([]string, 0, len(args)+2)
	if ctxName != "" {
		full = append(full, "--context", ctxName)
	}
	full = append(full, args...)
	var cmd *exec.Cmd
	if ctx.Done() == nil {
		cmd = exec.Command(k.Kubectl, full...)
	} else {
		cmd = exec.CommandContext(ctx, k.Kubectl, full...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			// the whole group: kubectl and whatever it started
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
				return cmd.Process.Kill()
			}
			return nil
		}
	}
	cmd.WaitDelay = WaitDelay
	cmd.Env = withKubeconfig(os.Environ(), k.Kubeconfig)
	return cmd
}

// Output runs kubectl until it exits or ctx ends and returns its stdout.
// stderr is kept apart (warnings such as client-side throttling must not end
// up in JSON) and only makes the error: its trimmed text, or "timeout after
// …" (wrapping ErrTimeout) when ctx's deadline passed. Calls are not
// serialized: use it for changes (delete, rollout restart) and for reads
// that need their own deadline.
func (k Client) Output(ctx context.Context, ctxName string, args ...string) ([]byte, error) {
	start := time.Now()
	cmd := k.Command(ctx, ctxName, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := wait(ctx, cmd.Run())
	if err == nil {
		return stdout.Bytes(), nil
	}
	if errors.Is(err, ErrTimeout) {
		return nil, fmt.Errorf("%w after %s", err, time.Since(start).Round(time.Second))
	}
	msg := strings.TrimSpace(stderr.String())
	if msg == "" {
		msg = err.Error()
	}
	return nil, errors.New(msg)
}

// wait turns cmd.Run's error into the call's result: ErrTimeout when ctx's
// deadline passed, nil when kubectl succeeded but a process it left behind
// held the pipes open past WaitDelay.
func wait(ctx context.Context, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return ErrTimeout
	case errors.Is(err, exec.ErrWaitDelay):
		return nil // kubectl exited 0; its output is complete
	}
	return err
}

// Get runs a read-only kubectl command against ctxName and returns stdout.
// It is bounded by Timeout (kubectl also gets --request-timeout) counted
// from when it starts, and waits for any other Get on the same context in
// this process to finish first. The error is stderr's last line, or
// ErrTimeout.
func (k Client) Get(ctxName string, args ...string) ([]byte, error) {
	mu := contextLock(k.Kubeconfig, ctxName)
	mu.Lock()
	defer mu.Unlock()

	t := k.Timeout
	if t == 0 {
		t = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), t)
	defer cancel()
	args = append(args[:len(args):len(args)], fmt.Sprintf("--request-timeout=%ds", max(1, int(t.Seconds())-1)))
	cmd := k.Command(ctx, ctxName, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := wait(ctx, cmd.Run()); err != nil {
		if errors.Is(err, ErrTimeout) {
			return nil, err
		}
		msg := lastLine(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return stdout.Bytes(), nil
}

// Run runs kubectl attached to this terminal (stdin, stdout, stderr), in the
// terminal's process group: for interactive commands (exec -it, debug,
// run -it, config). The error is cmd.Run's (an *exec.ExitError when kubectl
// failed: it already printed why).
func (k Client) Run(ctxName string, args ...string) error {
	cmd := k.Command(context.Background(), ctxName, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// RunningPods lists the names of the Running (not terminating) pods matching
// selector in ns, sorted.
func (k Client) RunningPods(ctxName, ns, selector string) ([]string, error) {
	ps, err := k.Running(ctxName, ns, selector)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	return names, nil
}

// Running is RunningPods with the whole pods (their containers too), sorted
// by name.
func (k Client) Running(ctxName, ns, selector string) ([]Pod, error) {
	if selector == "" {
		return nil, errors.New("workload has no matchLabels selector")
	}
	out, err := k.Get(ctxName, "-n", ns, "get", "pods", "-l", selector, "-o", "json")
	if err != nil {
		return nil, err
	}
	ps, err := ParsePods(out)
	if err != nil {
		return nil, err
	}
	var run []Pod
	for _, p := range ps {
		if p.Phase == "Running" && p.Reason != "Terminating" {
			run = append(run, p)
		}
	}
	sort.Slice(run, func(i, j int) bool { return run[i].Name < run[j].Name })
	return run, nil
}

// Exec runs command in a pod (container "" = the default one) and returns
// what it printed, stdout and stderr together, also when it fails. ctx
// bounds it.
func (k Client) Exec(ctx context.Context, ctxName, ns, pod, container string, command []string) (string, error) {
	args := []string{"-n", ns, "exec", pod}
	if container != "" {
		args = append(args, "-c", container)
	}
	args = append(append(args, "--"), command...)
	cmd := k.Command(ctx, ctxName, args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	start := time.Now()
	err := wait(ctx, cmd.Run())
	switch {
	case errors.Is(err, ErrTimeout):
		return buf.String(), fmt.Errorf("%w after %s", err, time.Since(start).Round(time.Second))
	case err != nil:
		msg := strings.TrimSpace(buf.String())
		if msg == "" {
			msg = err.Error()
		}
		return buf.String(), errors.New(msg)
	}
	return buf.String(), nil
}

var locks sync.Map // kubeconfig|context → *sync.Mutex

// contextLock is the mutex that serializes Get calls to one context.
func contextLock(kubeconfig, ctxName string) *sync.Mutex {
	mu, _ := locks.LoadOrStore(kubeconfig+"|"+ctxName, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

var nsCache sync.Map // kubeconfig|ctx → []string

// Namespaces lists namespaces (cached in memory per process).
func (k Client) Namespaces(ctxName string) ([]string, error) {
	key := k.Kubeconfig + "|" + ctxName
	if v, ok := nsCache.Load(key); ok {
		return v.([]string), nil
	}
	out, err := k.Get(ctxName, "get", "ns", "-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	nsCache.Store(key, names)
	return names, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// withKubeconfig returns env with KUBECONFIG set to path (replacing any value).
func withKubeconfig(env []string, path string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, "KUBECONFIG=") {
			out = append(out, e)
		}
	}
	return append(out, "KUBECONFIG="+path)
}
