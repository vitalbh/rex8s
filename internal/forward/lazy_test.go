package forward

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
)

// TestHelperKubectl is a fake `kubectl port-forward`: it echoes lines back
// on a port of its own. FAKE_KUBECTL_FAIL makes it fail, FAKE_KUBECTL_DELAY
// waits before forwarding. Each start appends a line to FAKE_KUBECTL_STARTS.
func TestHelperKubectl(t *testing.T) {
	if os.Getenv("FAKE_KUBECTL") == "" {
		return
	}
	if f, err := os.OpenFile(os.Getenv("FAKE_KUBECTL_STARTS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintln(f, "start")
		f.Close()
	}
	if os.Getenv("FAKE_KUBECTL_FAIL") != "" {
		fmt.Fprintln(os.Stderr, "E1117 12:00:00 portforward.go:1] some noise")
		fmt.Fprint(os.Stderr, `error: services "nope" not found`) // no newline: still the error
		os.Exit(1)
	}
	if d, err := time.ParseDuration(os.Getenv("FAKE_KUBECTL_DELAY")); err == nil {
		time.Sleep(d)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	fmt.Printf("Forwarding from 127.0.0.1:%d -> 80\n", l.Addr().(*net.TCPAddr).Port)
	fmt.Fprintln(os.Stderr, "Handling connection for 80")
	for {
		c, err := l.Accept()
		if err != nil {
			os.Exit(0)
		}
		go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
	}
}

// fakeClient is a kube.Client whose kubectl is TestHelperKubectl; env sets
// its behavior. It returns a counter of kubectl starts.
func fakeClient(t *testing.T, env ...string) (kube.Client, func() int) {
	t.Helper()
	dir := t.TempDir()
	starts := filepath.Join(dir, "starts")
	script := filepath.Join(dir, "kubectl")
	os.WriteFile(script, []byte("#!/bin/sh\nexec \""+os.Args[0]+"\" -test.run='^TestHelperKubectl$'\n"), 0o755)
	t.Setenv("FAKE_KUBECTL", "1")
	t.Setenv("FAKE_KUBECTL_STARTS", starts)
	for i := 0; i+1 < len(env); i += 2 {
		t.Setenv(env[i], env[i+1])
	}
	count := func() int {
		data, _ := os.ReadFile(starts)
		return strings.Count(string(data), "start")
	}
	return kube.Client{Kubectl: script, Kubeconfig: filepath.Join(dir, "kubeconfig")}, count
}

type logBuf struct {
	mu    sync.Mutex
	lines []string
}

func (b *logBuf) logf(format string, args ...any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, fmt.Sprintf(format, args...))
}

func (b *logBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
}

func fakePF(t *testing.T, kind Kind, env ...string) (*Lazy, string, func() int, *logBuf) {
	t.Helper()
	cl, count := fakeClient(t, env...)
	logs := &logBuf{}
	p := &Lazy{Client: cl, Context: "c", Namespace: "ns", Target: "svc/web", Remote: 80, Kind: kind,
		Idle: 200 * time.Millisecond, Tick: 50 * time.Millisecond, StartWait: 10 * time.Second, Logf: logs.logf}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go p.Serve(l)
	t.Cleanup(func() { l.Close(); p.Stop() })
	return p, l.Addr().String(), count, logs
}

func echo(t *testing.T, addr, msg string) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintln(c, msg)
	line, _ := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(line)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestLazyStartsOnUseAndStopsWhenIdle(t *testing.T) {
	p, addr, starts, logs := fakePF(t, KindTCP)
	time.Sleep(100 * time.Millisecond)
	if n := starts(); n != 0 {
		t.Fatalf("kubectl started %d times before any connection", n)
	}
	if got := echo(t, addr, "hello"); got != "hello" {
		t.Fatalf("echo = %q", got)
	}
	if got := echo(t, addr, "again"); got != "again" {
		t.Fatalf("echo = %q", got)
	}
	if n := starts(); n != 1 {
		t.Fatalf("kubectl started %d times for two connections, want 1", n)
	}
	waitFor(t, "kubectl still running after idle", func() bool { return !p.Running() })
	if got := echo(t, addr, "back"); got != "back" || starts() != 2 {
		t.Fatalf("after idle: echo = %q, starts = %d (want back, 2)", got, starts())
	}
	// kubectl's stderr reaches the log
	waitFor(t, "stderr not logged: "+logs.String(), func() bool { return strings.Contains(logs.String(), "kubectl: Handling connection for 80") })
}

func TestLazyFailureAnswers502ForWeb(t *testing.T) {
	_, addr, _, logs := fakePF(t, KindHTTP, "FAKE_KUBECTL_FAIL", "1")
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	body, _ := io.ReadAll(c)
	if !strings.HasPrefix(string(body), "HTTP/1.1 502") || !strings.Contains(string(body), `services "nope" not found`) || strings.Contains(string(body), "noise") {
		t.Fatalf("response = %q", body)
	}
	if !strings.Contains(logs.String(), "kubectl: E1117") {
		t.Errorf("stderr not logged: %s", logs)
	}
}

func TestLazyExpiresWhenUnused(t *testing.T) {
	cl, _ := fakeClient(t)
	gone := make(chan struct{})
	p := &Lazy{Client: cl, Context: "c", Namespace: "ns", Target: "svc/web", Remote: 80, Kind: KindTCP,
		Idle: time.Hour, Tick: 50 * time.Millisecond, Expire: 400 * time.Millisecond, OnExpire: func() { close(gone) }}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go p.Serve(l)
	t.Cleanup(func() { l.Close(); p.Stop() })
	if got := echo(t, l.Addr().String(), "used"); got != "used" { // a connection resets the clock
		t.Fatalf("echo = %q", got)
	}
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("forward did not expire")
	}
	if p.Running() {
		t.Error("kubectl runs after expiry")
	}
}

// Stop must not wait for a kubectl that is still starting (B19).
func TestLazyStopDuringStart(t *testing.T) {
	p, addr, starts, _ := fakePF(t, KindTCP, "FAKE_KUBECTL_DELAY", "5s")
	go func() {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			defer c.Close()
			c.SetDeadline(time.Now().Add(10 * time.Second))
			io.ReadAll(c)
		}
	}()
	waitFor(t, "kubectl never started", func() bool { return starts() == 1 })
	begin := time.Now()
	stopped := make(chan struct{})
	go func() { p.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked while kubectl was starting")
	}
	if d := time.Since(begin); d > time.Second {
		t.Fatalf("Stop took %s", d)
	}
	if _, err := p.backend(); err != ErrStopped {
		t.Errorf("backend after Stop: %v", err)
	}
}

// A client that keeps its side open must not keep a dead forward busy: when
// kubectl dies, both connections close and the forward can go idle (B19).
func TestLazyKubectlDeathClosesClient(t *testing.T) {
	p, addr, _, _ := fakePF(t, KindTCP)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(c)
	fmt.Fprintln(c, "ping")
	if l, _ := r.ReadString('\n'); strings.TrimSpace(l) != "ping" {
		t.Fatalf("echo = %q", l)
	}
	p.restart() // kubectl dies; the client stays connected
	if _, err := r.ReadString('\n'); err == nil {
		t.Fatal("client connection still open after kubectl died")
	}
	waitFor(t, "connection still counted as active", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.active == 0
	})
}

// Connections that arrive while kubectl starts wait for that start instead
// of starting their own.
func TestLazyConcurrentConnectionsShareOneStart(t *testing.T) {
	_, addr, starts, _ := fakePF(t, KindTCP, "FAKE_KUBECTL_DELAY", "300ms")
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if got := echo(t, addr, fmt.Sprint(i)); got != fmt.Sprint(i) {
				t.Errorf("echo %d = %q", i, got)
			}
		}(i)
	}
	wg.Wait()
	if n := starts(); n != 1 {
		t.Errorf("kubectl started %d times, want 1", n)
	}
}

func TestLineWriterBounded(t *testing.T) {
	var got []string
	w := &lineWriter{line: func(l string) { got = append(got, l) }}
	w.Write([]byte("one\ntw"))
	w.Write([]byte("o\n" + strings.Repeat("x", 3*maxLine)))
	w.Close()
	if len(got) != 5 || got[0] != "one" || got[1] != "two" || len(got[2]) != maxLine || len(w.buf) != 0 {
		t.Fatalf("lines %d: %.20q", len(got), got)
	}
	tl := &tailLines{max: 2}
	for _, l := range []string{"a", "b", "c"} {
		tl.add(l)
	}
	if tl.last() != "c" || len(tl.lines) != 2 {
		t.Errorf("tail = %v", tl.lines)
	}
}

func TestDetectKind(t *testing.T) {
	for _, c := range []struct {
		p    kube.ServicePort
		want Kind
	}{
		{kube.ServicePort{Name: "http", Port: 1234}, KindHTTP},
		{kube.ServicePort{Name: "tcp-postgresql", Port: 1}, KindPostgres},
		{kube.ServicePort{AppProtocol: "kubernetes.io/h2c", Port: 80}, KindGRPC},
		{kube.ServicePort{Port: 6379}, KindRedis},
		{kube.ServicePort{Name: "grpc-web", Port: 8080}, KindGRPC},
		{kube.ServicePort{Port: 12345}, KindTCP},
		// ui and web are whole words only (B19)
		{kube.ServicePort{Name: "ui", Port: 1}, KindHTTP},
		{kube.ServicePort{Name: "web-ui", Port: 1}, KindHTTP},
		{kube.ServicePort{Name: "admin_web", Port: 1}, KindHTTP},
		{kube.ServicePort{Name: "quic", Port: 1}, KindTCP},
		{kube.ServicePort{Name: "build", Port: 1}, KindTCP},
		{kube.ServicePort{Name: "druid", Port: 1}, KindTCP},
		{kube.ServicePort{Name: "webhook", Port: 1}, KindTCP},
		{kube.ServicePort{Name: "webhook", Port: 443}, KindHTTPS},
	} {
		if got := DetectKind(c.p); got != c.want {
			t.Errorf("DetectKind(%+v) = %s, want %s", c.p, got, c.want)
		}
	}
}
