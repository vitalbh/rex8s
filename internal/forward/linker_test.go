package forward

import (
	"fmt"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"
)

var oscRe = regexp.MustCompile("\x1b\\]8;;([^\x1b]*)\x1b\\\\([^\x1b]*)\x1b\\]8;;\x1b\\\\")

// links returns the (target, text) of each OSC 8 link in s.
func links(s string) [][2]string {
	var out [][2]string
	for _, m := range oscRe.FindAllStringSubmatch(s, -1) {
		out = append(out, [2]string{m[1], m[2]})
	}
	return out
}

func newLinker(t *testing.T) *Linker {
	l := &Linker{Context: "c", Namespace: "app", AllNamespaces: true}
	t.Cleanup(l.Close)
	return l
}

func (l *Linker) has(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.links[key]
	return ok
}

func TestLinker(t *testing.T) {
	l := newLinker(t)
	got := l.Line("calling http://grafana.monitoring.svc.cluster.local:3000/d/abc?x=1.", "")
	ls := links(got)
	if len(ls) != 1 || ls[0][1] != "http://grafana.monitoring.svc.cluster.local:3000/d/abc?x=1" ||
		!strings.HasPrefix(ls[0][0], "http://localhost:") || !strings.HasSuffix(ls[0][0], "/d/abc?x=1") ||
		!strings.HasSuffix(got, ".") {
		t.Fatalf("svc URL: %q → %q", got, ls)
	}
	if !l.has("monitoring|svc/grafana|3000") {
		t.Fatalf("links = %v", l.links)
	}

	// same service again: same local port
	again := links(l.Line("GET http://grafana.monitoring.svc:3000/", ""))
	if len(l.links) != 1 || !strings.HasPrefix(again[0][0], strings.TrimSuffix(ls[0][0], "/d/abc?x=1")) {
		t.Fatalf("second link %q, first %q, links %v", again[0][0], ls[0][0], l.links)
	}

	// no port: 80 for http
	l.Line("http://api.payments.svc/health", "")
	if !l.has("payments|svc/api|80") {
		t.Fatalf("default port: %v", l.links)
	}

	// the pod that logged "listening": from kubectl's --prefix
	l.Line("[pod/web-7d9f/app] Listening on http://0.0.0.0:8080", "")
	if !l.has("app|pod/web-7d9f|8080") {
		t.Fatalf("pod prefix: %v", l.links)
	}
	// or given (single-pod logs)
	l.Line("ready at http://localhost:3000/", "api-0")
	if !l.has("app|pod/api-0|3000") {
		t.Fatalf("pod given: %v", l.links)
	}
	// a svc URL in single-pod logs: one link, not a link inside the link
	if ls := links(l.Line("upstream http://web.app.svc:80/login.", "api-0")); len(ls) != 1 ||
		ls[0][1] != "http://web.app.svc:80/login" {
		t.Fatalf("svc in pod logs: %q", ls)
	}
	// localhost without knowing the pod: left alone
	if s := "see http://localhost:9999"; l.Line(s, "") != s {
		t.Fatal("localhost without a pod was linked")
	}
	var nilL *Linker
	if nilL.Line("http://a.b.svc", "") != "http://a.b.svc" {
		t.Fatal("nil linker changed the line")
	}
}

// Hosts that only look like a service stay as they are (B8).
func TestLinkerOnlyClusterHosts(t *testing.T) {
	l := newLinker(t)
	for _, s := range []string{
		"fetching https://example.com/a.svc",
		"http://api.prod.svc.k8s.local:8080/x",
		"https://docs.team.svcs.example.com/path",
		"http://a.b.svc.cluster.localdomain/foo",
		"http://a.b.svcx/foo",
		"http://-a.b.svc/",
	} {
		if got := l.Line(s, ""); got != s {
			t.Errorf("%q linked: %q", s, got)
		}
	}
	if n := l.Held(); n != 0 {
		t.Errorf("%d ports held for hosts outside the cluster", n)
	}
	// a cluster with its own domain
	l.ClusterDomain = "k8s.local"
	if ls := links(l.Line("http://api.prod.svc.k8s.local:8080/x", "")); len(ls) != 1 || ls[0][1] != "http://api.prod.svc.k8s.local:8080/x" {
		t.Errorf("own domain: %q", ls)
	}
	// the host ends where it ends
	if ls := links(l.Line("see http://a.b.svc.", "")); len(ls) != 1 || ls[0][1] != "http://a.b.svc" {
		t.Errorf("sentence end: %q", ls)
	}
}

// By default only the stream's namespace is linked (B9).
func TestLinkerNamespace(t *testing.T) {
	l := &Linker{Context: "c", Namespace: "app"}
	t.Cleanup(l.Close)
	if s := "http://vault.vault.svc:8200/v1"; l.Line(s, "") != s {
		t.Error("service of another namespace linked")
	}
	if ls := links(l.Line("http://web.app.svc/", "")); len(ls) != 1 {
		t.Errorf("own namespace: %q", ls)
	}
}

// At most Max ports; the least recently used one goes (B9).
func TestLinkerCap(t *testing.T) {
	l := &Linker{Context: "c", Namespace: "app", Max: 3}
	t.Cleanup(l.Close)
	port := func(svc string) string {
		ls := links(l.Line("http://"+svc+".app.svc/", ""))
		if len(ls) != 1 {
			t.Fatalf("%s: %q", svc, ls)
		}
		return ls[0][0]
	}
	a := port("a")
	time.Sleep(5 * time.Millisecond)
	port("b")
	time.Sleep(5 * time.Millisecond)
	port("c")
	time.Sleep(5 * time.Millisecond)
	if port("a") != a { // a used again: b is now the oldest
		t.Fatal("a got a new port")
	}
	time.Sleep(5 * time.Millisecond)
	port("d")
	if l.Held() != 3 || l.has("app|svc/b|80") || !l.has("app|svc/a|80") {
		t.Fatalf("after d: %d held, links %v", l.Held(), l.links)
	}
	// b's port was closed
	for i := 0; i < 50; i++ {
		port(fmt.Sprintf("s%d", i))
	}
	if l.Held() != 3 {
		t.Errorf("held %d", l.Held())
	}
	addr := strings.TrimPrefix(strings.TrimSuffix(a, "/"), "http://")
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Errorf("evicted port %s still listens", addr)
	}
}

func TestLinkerPrune(t *testing.T) {
	l := newLinker(t)
	l.Line("Listening on http://0.0.0.0:8080", "web-1")
	l.Line("Listening on http://0.0.0.0:8080", "web-2")
	l.Line("http://web.app.svc/", "web-2")
	l.Prune(func(pod string) bool { return pod == "web-2" })
	if l.has("app|pod/web-1|8080") || !l.has("app|pod/web-2|8080") || !l.has("app|svc/web|80") {
		t.Errorf("after prune: %v", l.links)
	}
}
