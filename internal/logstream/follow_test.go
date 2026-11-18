package logstream

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
)

// fakeCluster is a kubectl stand-in: `get pods` prints dir/pods.json (or
// dir/err on stderr, exit 1, when that file exists); `logs` prints a line
// naming dir/gen, then waits like -f. Every call is appended to dir/calls.
type fakeCluster struct {
	t   *testing.T
	dir string
}

func newFakeCluster(t *testing.T) (*fakeCluster, kube.Client) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
D='` + dir + `'
echo "$*" >> "$D/calls"
case "$*" in
*" get pods "*)
	if [ -f "$D/err" ]; then cat "$D/err" >&2; exit 1; fi
	cat "$D/pods.json" ;;
*" logs "*)
	echo "[pod/web-1/web] hello from $(cat "$D/gen")"
	exec sleep 30 ;;
esac
`
	bin := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeCluster{t, dir}
	f.write("gen", "gen1")
	f.pods()
	return f, kube.Client{Kubectl: bin, Kubeconfig: filepath.Join(dir, "config")}
}

func (f *fakeCluster) write(name, s string) {
	f.t.Helper()
	tmp := filepath.Join(f.dir, name+".tmp")
	if err := os.WriteFile(tmp, []byte(s), 0o644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(f.dir, name)); err != nil {
		f.t.Fatal(err)
	}
}

// pods makes `get pods` list these Running pods, two containers each.
func (f *fakeCluster) pods(names ...string) {
	type item struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Containers []map[string]string `json:"containers"`
		} `json:"spec"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}
	var list struct {
		Items []item `json:"items"`
	}
	list.Items = []item{}
	for _, n := range names {
		var it item
		it.Metadata.Name = n
		it.Spec.Containers = []map[string]string{{"name": "web"}, {"name": "exporter"}}
		it.Status.Phase = "Running"
		list.Items = append(list.Items, it)
	}
	b, _ := json.Marshal(list)
	f.write("pods.json", string(b))
}

func (f *fakeCluster) calls() string {
	b, _ := os.ReadFile(filepath.Join(f.dir, "calls"))
	return string(b)
}

// waitFor polls cond until it holds or 10s pass.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// follow runs f.Follow in the background; the returned func stops it and
// waits for it to return.
func follow(t *testing.T, f *Follower, s Spec) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.Follow(ctx, s, StreamLabel(s.Label, "39", false))
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Follow did not stop")
		}
	}
}

// When the pods change (a rollout), the stream starts again so the new set
// is followed, from 30s back; --max-log-requests follows the pod count.
func TestFollowRestartsWhenPodsChange(t *testing.T) {
	fc, cl := newFakeCluster(t)
	fc.pods("web-1")
	var out syncBuffer
	f := &Follower{Client: cl, Out: &out, Poll: 50 * time.Millisecond, Retry: 100 * time.Millisecond}
	stop := follow(t, f, Spec{"use-admin", "app", "app=web", "use web"})
	defer stop()

	waitFor(t, "the first stream", func() bool { return strings.Contains(out.String(), "hello from gen1") })
	fc.write("gen", "gen2")
	pods := make([]string, 30)
	for i := range pods {
		pods[i] = "web-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	fc.pods(pods...)
	waitFor(t, "the restart", func() bool { return strings.Contains(out.String(), "hello from gen2") })
	stop()

	got := plain(out.String())
	if !strings.Contains(got, "── pods changed, following the new set ──") {
		t.Fatalf("no restart notice:\n%s", got)
	}
	var logs []string
	for _, l := range strings.Split(fc.calls(), "\n") {
		if strings.Contains(l, " logs ") {
			logs = append(logs, l)
		}
	}
	if len(logs) < 2 {
		t.Fatalf("kubectl logs ran %d times:\n%s", len(logs), fc.calls())
	}
	if !strings.Contains(logs[0], "--max-log-requests=50") || !strings.Contains(logs[0], "--tail=30") {
		t.Errorf("first: %s", logs[0])
	}
	// 30 pods × (2 containers + 1 init allowance)
	if !strings.Contains(logs[1], "--max-log-requests=90") || !strings.Contains(logs[1], "--since=30s") {
		t.Errorf("after the rollout: %s", logs[1])
	}
	if !strings.HasPrefix(logs[0], "--context use-admin -n app logs -f -l app=web --prefix --all-containers") {
		t.Errorf("args: %s", logs[0])
	}
}

// When the pods cannot be listed, the real error is said — once, not at
// every retry — with the kauth hint when the login expired (B12); the
// stream starts when the cluster answers again.
func TestFollowSaysWhyPodsFail(t *testing.T) {
	fc, cl := newFakeCluster(t)
	fc.write("err", "error: You must be logged in to the server (Unauthorized)\n")
	var out syncBuffer
	f := &Follower{Client: cl, Out: &out, Poll: 50 * time.Millisecond, Retry: 30 * time.Millisecond,
		Clusters: map[string]kube.Context{"use-admin": {Name: "use-admin", Short: "use"}}}
	stop := follow(t, f, Spec{"use-admin", "app", "app=web", "use web"})
	defer stop()

	waitFor(t, "a few retries", func() bool { return strings.Count(fc.calls(), " get pods ") >= 4 })
	fc.write("err", "Unable to connect to the server: dial tcp: lookup api.example.com: no such host\n")
	waitFor(t, "the second error", func() bool { return strings.Contains(out.String(), "no such host") })
	waitFor(t, "a few more retries", func() bool { return strings.Count(fc.calls(), " get pods ") >= 8 })
	os.Remove(filepath.Join(fc.dir, "err"))
	waitFor(t, "no pods", func() bool { return strings.Contains(out.String(), "no running pods") })
	fc.pods("web-1")
	waitFor(t, "the stream", func() bool { return strings.Contains(out.String(), "hello from gen1") })
	stop()

	got := plain(out.String())
	if n := strings.Count(got, "(login expired · run kauth use; retrying)"); n != 1 {
		t.Errorf("auth error said %d times:\n%s", n, got)
	}
	if n := strings.Count(got, "(cannot list pods: Unable to connect to the server: dial tcp: lookup api.example.com: no such host; retrying)"); n != 1 {
		t.Errorf("connection error said %d times:\n%s", n, got)
	}
	if strings.Count(got, "(no running pods; retrying)") != 1 {
		t.Errorf("no pods:\n%s", got)
	}
}

// kubectl that cannot start is said, not a silent return.
func TestFollowPodStartFails(t *testing.T) {
	var out syncBuffer
	f := &Follower{Client: kube.Client{Kubectl: filepath.Join(t.TempDir(), "no-kubectl")}, Out: &out}
	f.Follow(context.Background(), Spec{"use-admin", "app", PodSelector + "web-1", "use web-1"}, nil)
	if got := out.String(); !strings.Contains(got, "cannot start kubectl for pod web-1") {
		t.Fatalf("got %q", got)
	}
}

// A single pod: its lines, led by the pod.
func TestFollowPod(t *testing.T) {
	_, cl := newFakeCluster(t)
	var out syncBuffer
	f := &Follower{Client: cl, Out: &out}
	stop := follow(t, f, Spec{"use-admin", "app", PodSelector + "web-1", "use web-1"})
	waitFor(t, "the stream", func() bool { return strings.Contains(out.String(), "hello from gen1") })
	stop()
	if got := plain(out.String()); !strings.HasPrefix(got, "web-1 hello from gen1\n") {
		t.Fatalf("got %q", got)
	}
}

func TestMaxLogRequests(t *testing.T) {
	if n := MaxLogRequests(nil); n != 50 {
		t.Errorf("none: %d", n)
	}
	ps := make([]kube.Pod, 40)
	for i := range ps {
		ps[i].Containers = []string{"a", "b", "c"}
	}
	if n := MaxLogRequests(ps); n != 160 {
		t.Errorf("40 pods × 3: %d", n)
	}
}
