package fleet

import (
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	api "k8s.io/client-go/tools/clientcmd/api"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/testkube"
)

func TestUnique(t *testing.T) {
	got := Unique([]kube.Context{
		{Name: "use-1", Server: "https://a"}, {Name: "use-2", Server: "https://a"}, {Name: "euw", Server: "https://b"}, {Name: "local"},
	})
	if len(got) != 3 || got[0].Name != "use-1" || got[1].Name != "euw" || got[2].Name != "local" {
		t.Errorf("aliases of the same server should collapse: %+v", got)
	}
}

func testConfig() (*api.Config, *config.Config) {
	raw := api.NewConfig()
	for name, server := range map[string]string{
		"acme-prod-use": "https://k8s.us-east.example.com", "acme-prod-use-admin": "https://k8s.us-east.example.com",
		"acme-prod-euw": "https://k8s.eu-west.example.com", "hidden": "https://k8s.lab.example.com", "euw": "https://k8s.other.example.com",
	} {
		raw.Clusters[name] = &api.Cluster{Server: server}
		raw.Contexts[name] = &api.Context{Cluster: name}
	}
	cfg := &config.Config{Hide: []string{"hidden"}, Clusters: []config.Cluster{
		{Match: "us-east", Name: "use", Prod: true}, {Match: "eu-west", Name: "euw", Prod: true}, {Match: "lab", Name: "lab"},
	}}
	return raw, cfg
}

func TestResolve(t *testing.T) {
	raw, cfg := testConfig()
	cases := map[string]string{
		"acme-prod-euw": "acme-prod-euw", // exact context name
		"use":           "acme-prod-use", // short name: the first visible one
		"hidden":        "hidden",        // hidden, but named exactly
		"euw":           "euw",           // a context named like another's short name wins
	}
	for name, want := range cases {
		c, err := Resolve(raw, cfg, name)
		if err != nil || c.Name != want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", name, c.Name, err, want)
		}
	}
	for _, name := range []string{"", "lab", "nope"} { // lab: only a hidden context has it
		if _, err := Resolve(raw, cfg, name); err == nil {
			t.Errorf("Resolve(%q) should fail", name)
		}
	}
}

func TestPick(t *testing.T) {
	raw, cfg := testConfig()
	all, err := Pick(raw, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range all {
		names = append(names, c.Name)
	}
	// visible, one per server (the admin alias of use goes)
	if strings.Join(names, ",") != "acme-prod-euw,euw,acme-prod-use" {
		t.Errorf("all = %v", names)
	}
	some, err := Pick(raw, cfg, []string{"use", "acme-prod-euw"})
	if err != nil || len(some) != 2 || some[0].Short != "use" || some[1].Name != "acme-prod-euw" {
		t.Errorf("named = %+v, %v", some, err)
	}
	if _, err := Pick(raw, cfg, []string{"use", "nope"}); err == nil {
		t.Error("an unknown name should fail")
	}
}

func TestSort(t *testing.T) {
	use, euw := kube.Context{Short: "use"}, kube.Context{Short: "euw"}
	rows := []kube.On[kube.Pod]{
		{C: use, V: kube.Pod{Namespace: "b", Name: "x"}}, {C: euw, V: kube.Pod{Namespace: "z", Name: "a"}},
		{C: use, V: kube.Pod{Namespace: "a", Name: "y"}}, {C: use, V: kube.Pod{Namespace: "a", Name: "b"}},
	}
	Sort(rows, func(p kube.Pod) (string, string) { return p.Namespace, p.Name })
	var got []string
	for _, r := range rows {
		got = append(got, r.C.Short+" "+r.V.Namespace+"/"+r.V.Name)
	}
	if strings.Join(got, ",") != "euw z/a,use a/b,use a/y,use b/x" {
		t.Errorf("sorted = %v", got)
	}
}

// fakeKubectl answers by context: ok-* print one pod, expired fails with an
// auth error, slow never answers, garbage prints non-JSON. Every call is
// counted in a file.
func fakeKubectl(t *testing.T) (kube.Client, func() int) {
	t.Helper()
	k := testkube.New(t).
		On("ok-*", "*", testkube.Reply{Stdout: `{"items":[{"metadata":{"name":"web-1","namespace":"app"},"status":{"phase":"Running"}}]}`}).
		On("expired", "*", testkube.Reply{Stderr: "error: You must be logged in to the server (Unauthorized)\n", Exit: 1}).
		On("slow", "*", testkube.Reply{Delay: 10 * time.Second}).
		On("garbage", "*", testkube.Reply{Stdout: "not json\n"}).
		Default(testkube.Reply{Shell: `echo "error: context $2 does not exist" >&2`, Exit: 1})
	count := func() int { return k.Count("") }
	return kube.Client{Kubectl: k.Path, Kubeconfig: k.Kubeconfig, Timeout: 10 * time.Second}, count
}

func pods(_ kube.Context, out [][]byte) ([]kube.Pod, error) { return kube.ParsePods(out[0]) }

var podQuery = [][]string{{"get", "pods", "-A", "-o", "json"}}

func TestRead(t *testing.T) {
	cl, _ := fakeKubectl(t)
	r := &Reader{Client: cl}
	clusters := []kube.Context{{Name: "ok-1", Short: "use"}, {Name: "expired", Short: "euw"}, {Name: "garbage", Short: "sa"}, {Name: "ok-2", Short: "stg"}, {Name: "gone", Short: "lab"}}
	var each atomic.Int32
	res := ReadEach(r, clusters, podQuery, pods, func(c kube.Context, rows []kube.On[kube.Pod], skip *Skip) {
		each.Add(1)
		if (skip == nil) != strings.HasPrefix(c.Name, "ok-") {
			t.Errorf("%s: rows=%v skip=%v", c.Name, rows, skip)
		}
	})
	if each.Load() != 5 {
		t.Errorf("callback ran %d times, want 5", each.Load())
	}
	if res.OK != 2 || len(res.Rows) != 2 || res.Rows[0].C.Short != "use" || res.Rows[1].C.Short != "stg" || res.Rows[0].V.Name != "web-1" {
		t.Errorf("rows = %+v", res)
	}
	for _, s := range res.Skipped {
		if s.Expired != (s.Cluster.Name == "expired") {
			t.Errorf("%s: Expired = %v", s.Cluster.Name, s.Expired)
		}
	}
	got := strings.Join(res.Strings(), " | ")
	for _, want := range []string{"euw: login expired (kauth euw)", "sa: invalid character", "lab: error: context gone does not exist"} {
		if !strings.Contains(got, want) {
			t.Errorf("skipped %q, want %q in it", got, want)
		}
	}
}

// An unreachable cluster fails within the probe and is then backed off: the
// next reads skip it without running kubectl, until Retry (B7).
func TestUnreachableIsBackedOff(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	l.Close()
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(k, "")
	}

	cl, calls := fakeKubectl(t)
	r := &Reader{Client: cl, Probe: time.Second}
	down := kube.Context{Name: "ok-down", Short: "euw", Server: "https://" + closed}
	up := kube.Context{Name: "ok-up", Short: "use"} // no server: not probed
	res := Read(r, []kube.Context{up, down}, podQuery, pods)
	if res.OK != 1 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "unreachable") {
		t.Fatalf("first read: %+v", res)
	}
	if calls() != 1 {
		t.Errorf("kubectl ran %d times, want 1 (not for the unreachable one)", calls())
	}
	s := res.Skipped[0]
	if s.Retry.Sub(s.Since) != backoffMin {
		t.Errorf("first backoff = %s", s.Retry.Sub(s.Since))
	}

	res = Read(r, []kube.Context{up, down}, podQuery, pods)
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].String(), "next try") {
		t.Fatalf("second read: %+v", res.Strings())
	}

	r.fail(down, "unreachable") // a second failure in a row waits longer
	if f := r.state[Key(down)]; f.retry.Sub(f.since) < 2*backoffMin {
		t.Errorf("a second failure should wait %s: %s", 2*backoffMin, f.retry.Sub(f.since))
	}
	r.Retry()
	if r.backedOff(down) != nil {
		t.Error("Retry clears the backoff")
	}
	r.fail(down, "unreachable")
	r.ok(down)
	if r.backedOff(down) != nil {
		t.Error("an answer clears the backoff")
	}
}

func TestTimeoutIsBackedOff(t *testing.T) {
	cl, calls := fakeKubectl(t)
	cl.Timeout = time.Second
	r := &Reader{Client: cl}
	slow := kube.Context{Name: "slow", Short: "sa"}
	start := time.Now()
	res := Read(r, []kube.Context{slow}, podQuery, pods)
	if time.Since(start) > 3*time.Second || len(res.Skipped) != 1 || res.Skipped[0].Reason != "timeout" {
		t.Fatalf("first read: %+v after %s", res, time.Since(start))
	}
	// the first call may be killed before the fake records it on a loaded
	// machine: compare before and after the second read
	time.Sleep(200 * time.Millisecond)
	before := calls()
	Read(r, []kube.Context{slow}, podQuery, pods)
	time.Sleep(200 * time.Millisecond)
	if calls() != before {
		t.Errorf("kubectl ran again (%d → %d); the timed-out cluster should be backed off", before, calls())
	}
}

func TestServerAddr(t *testing.T) {
	for in, want := range map[string]string{
		"https://k8s.example.com:6443": "k8s.example.com:6443",
		"https://k8s.example.com":      "k8s.example.com:443",
		"http://10.0.0.1":              "10.0.0.1:80",
		"https://[::1]:6443":           "[::1]:6443",
		"unix:///var/run/k8s.sock":     "",
		"":                             "",
	} {
		if got := serverAddr(in); got != want {
			t.Errorf("serverAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
