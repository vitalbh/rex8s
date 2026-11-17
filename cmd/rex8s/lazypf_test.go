package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHelperKubectl is a fake `kubectl port-forward`: it echoes lines back
// on a port of its own, or fails when FAKE_KUBECTL_FAIL is set. Each start
// appends a line to FAKE_KUBECTL_STARTS.
func TestHelperKubectl(t *testing.T) {
	if os.Getenv("FAKE_KUBECTL") == "" {
		return
	}
	if f, err := os.OpenFile(os.Getenv("FAKE_KUBECTL_STARTS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintln(f, "start")
		f.Close()
	}
	if os.Getenv("FAKE_KUBECTL_FAIL") != "" {
		fmt.Fprintln(os.Stderr, `error: services "nope" not found`)
		os.Exit(1)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	fmt.Printf("Forwarding from 127.0.0.1:%d -> 80\n", l.Addr().(*net.TCPAddr).Port)
	for {
		c, err := l.Accept()
		if err != nil {
			os.Exit(0)
		}
		go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
	}
}

func fakePF(t *testing.T, fail bool, kind portKind) (*lazyPF, string, func() int) {
	t.Helper()
	dir := t.TempDir()
	starts := filepath.Join(dir, "starts")
	script := filepath.Join(dir, "kubectl")
	os.WriteFile(script, []byte("#!/bin/sh\nexec \""+os.Args[0]+"\" -test.run='^TestHelperKubectl$'\n"), 0o755)
	env := append(os.Environ(), "FAKE_KUBECTL=1", "FAKE_KUBECTL_STARTS="+starts)
	if fail {
		env = append(env, "FAKE_KUBECTL_FAIL=1")
	}
	p := &lazyPF{kubectl: script, env: env, ctx: "c", ns: "ns", target: "svc/web", remote: 80, kind: kind,
		idle: 200 * time.Millisecond, tick: 50 * time.Millisecond, startWait: 10 * time.Second,
		logf: func(string, ...any) {}, lastUse: time.Now()}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go p.serve(l)
	t.Cleanup(func() { l.Close(); p.stop() })
	count := func() int {
		data, _ := os.ReadFile(starts)
		return strings.Count(string(data), "start")
	}
	return p, l.Addr().String(), count
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

func TestLazyPFStartsOnUseAndStopsWhenIdle(t *testing.T) {
	p, addr, starts := fakePF(t, false, kindTCP)
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
	deadline := time.Now().Add(3 * time.Second)
	for {
		p.mu.Lock()
		running := p.cmd != nil
		p.mu.Unlock()
		if !running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("kubectl still running after idle")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := echo(t, addr, "back"); got != "back" || starts() != 2 {
		t.Fatalf("after idle: echo = %q, starts = %d (want back, 2)", got, starts())
	}
}

func TestLazyPFFailureAnswers502ForWeb(t *testing.T) {
	_, addr, _ := fakePF(t, true, kindHTTP)
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	body, _ := io.ReadAll(c)
	if !strings.HasPrefix(string(body), "HTTP/1.1 502") || !strings.Contains(string(body), `services "nope" not found`) {
		t.Fatalf("response = %q", body)
	}
}

func TestDetectKind(t *testing.T) {
	for _, c := range []struct {
		p    svcPort
		want portKind
	}{
		{svcPort{Name: "http", Port: 1234}, kindHTTP},
		{svcPort{Name: "tcp-postgresql", Port: 1}, kindPostgres},
		{svcPort{AppProtocol: "kubernetes.io/h2c", Port: 80}, kindGRPC},
		{svcPort{Port: 6379}, kindRedis},
		{svcPort{Name: "grpc-web", Port: 8080}, kindGRPC},
		{svcPort{Port: 12345}, kindTCP},
	} {
		if got := detectKind(c.p); got != c.want {
			t.Errorf("detectKind(%+v) = %s, want %s", c.p, got, c.want)
		}
	}
}

func TestLazyPFExpiresWhenUnused(t *testing.T) {
	p, addr, _ := fakePF(t, false, kindTCP)
	gone := make(chan struct{})
	p.mu.Lock()
	p.expire, p.onExpire = 400*time.Millisecond, func() { close(gone) }
	p.mu.Unlock()
	if got := echo(t, addr, "used"); got != "used" { // a connection resets the clock
		t.Fatalf("echo = %q", got)
	}
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("forward did not expire")
	}
}

func TestShortDur(t *testing.T) {
	for d, want := range map[time.Duration]string{10 * time.Minute: "10m", 8 * time.Hour: "8h", 90 * time.Minute: "1h30m"} {
		if got := shortDur(d); got != want {
			t.Errorf("shortDur(%v) = %q, want %q", d, got, want)
		}
	}
}
