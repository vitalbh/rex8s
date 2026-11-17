package main

import (
	"regexp"
	"strings"
	"testing"
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

func TestLogLinker(t *testing.T) {
	l := &logLinker{kubectl: "kubectl", ctx: "c", ns: "app", ports: map[string]int{}}
	got := l.line("calling http://grafana.monitoring.svc.cluster.local:3000/d/abc?x=1.", "")
	ls := links(got)
	if len(ls) != 1 || ls[0][1] != "http://grafana.monitoring.svc.cluster.local:3000/d/abc?x=1" ||
		!strings.HasPrefix(ls[0][0], "http://localhost:") || !strings.HasSuffix(ls[0][0], "/d/abc?x=1") ||
		!strings.HasSuffix(got, ".") {
		t.Fatalf("svc URL: %q → %q", got, ls)
	}
	if _, ok := l.ports["monitoring|svc/grafana|3000"]; !ok {
		t.Fatalf("ports = %v", l.ports)
	}

	// same service again: same local port
	again := links(l.line("GET http://grafana.monitoring.svc:3000/", ""))
	if len(l.ports) != 1 || !strings.HasPrefix(again[0][0], strings.TrimSuffix(ls[0][0], "/d/abc?x=1")) {
		t.Fatalf("second link %q, first %q, ports %v", again[0][0], ls[0][0], l.ports)
	}

	// no port: 80 for http
	l.line("http://api.payments.svc/health", "")
	if _, ok := l.ports["payments|svc/api|80"]; !ok {
		t.Fatalf("default port: %v", l.ports)
	}

	// the pod that logged "listening": from kubectl's --prefix
	l.line("[pod/web-7d9f/app] Listening on http://0.0.0.0:8080", "")
	if _, ok := l.ports["app|pod/web-7d9f|8080"]; !ok {
		t.Fatalf("pod prefix: %v", l.ports)
	}
	// or given (single-pod logs)
	l.line("ready at http://localhost:3000/", "api-0")
	if _, ok := l.ports["app|pod/api-0|3000"]; !ok {
		t.Fatalf("pod given: %v", l.ports)
	}
	// a svc URL in single-pod logs: one link, not a link inside the link
	if ls := links(l.line("upstream http://web.app.svc:80/login.", "api-0")); len(ls) != 1 ||
		ls[0][1] != "http://web.app.svc:80/login" {
		t.Fatalf("svc in pod logs: %q", ls)
	}
	// localhost without knowing the pod: left alone
	if s := "see http://localhost:9999"; l.line(s, "") != s {
		t.Fatal("localhost without a pod was linked")
	}
	// ordinary URLs: left alone
	if s := "fetching https://example.com/a.svc"; l.line(s, "") != s {
		t.Fatal("ordinary URL was linked")
	}
	var nilL *logLinker
	if nilL.line("http://a.b.svc", "") != "http://a.b.svc" {
		t.Fatal("nil linker changed the line")
	}
}

func TestStreamLabel(t *testing.T) {
	strip := regexp.MustCompile("\x1b\\[[0-9;]*m")
	multi := streamLabel(logSpec{label: "euw web"}, "39", true)
	for _, c := range []struct{ pod, container, want string }{
		{"web-7d9f", "web", "euw web-7d9f "},
		{"web-7d9f", "exporter", "euw web-7d9f/exporter "},
		{"", "", "euw web "},
	} {
		if got := strip.ReplaceAllString(multi(c.pod, c.container), ""); got != c.want {
			t.Errorf("multi(%q, %q) = %q, want %q", c.pod, c.container, got, c.want)
		}
	}
	one := streamLabel(logSpec{label: "euw web"}, "39", false)
	if got := strip.ReplaceAllString(one("web-7d9f", "web"), ""); got != "web-7d9f " {
		t.Errorf("single = %q", got)
	}
	if one("", "") != "" {
		t.Error("single without pod should be empty")
	}
}

func TestGrepKeepsLinks(t *testing.T) {
	l := &logLinker{kubectl: "kubectl", ctx: "c", ns: "app", ports: map[string]int{}}
	re := regexp.MustCompile("(?i)grafana")
	line := highlightOutsideLinks(l.line("upstream http://grafana.monitoring.svc:3000/login", ""), re)
	ls := links(regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(line, ""))
	if len(ls) != 1 || !strings.HasPrefix(ls[0][0], "http://localhost:") {
		t.Fatalf("links after grep highlight: %q in %q", ls, line)
	}
	if strings.Contains(oscRe.FindStringSubmatch(line)[1], "\x1b[") {
		t.Fatalf("highlight inside the link target: %q", line)
	}
}
