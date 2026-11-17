package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// lazyPF holds a local port for a service and runs `kubectl port-forward`
// only while someone uses it: the first connection starts kubectl (on a
// random port of its own), connections are copied to it, and after idle
// without connections kubectl stops until the next one. When kubectl dies
// (the pod went away), the next connection starts it again.
type lazyPF struct {
	kubectl   string
	env       []string
	ctx, ns   string
	target    string // svc/NAME or pod/NAME
	remote    int
	kind      portKind
	idle      time.Duration
	logf      func(format string, args ...any)
	startWait time.Duration
	tick      time.Duration // how often idle is checked (0: 15s)
	expire    time.Duration // no connection this long: onExpire (0: never)
	onExpire  func()

	mu      sync.Mutex
	cmd     *exec.Cmd
	port    int // kubectl's local port while it runs
	exited  chan struct{}
	active  int
	lastUse time.Time
}

var forwardingRe = regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+) ->`)

// backend returns kubectl's port, starting kubectl when it is not running.
func (p *lazyPF) backend() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		select {
		case <-p.exited:
			p.cmd = nil
		default:
			return p.port, nil
		}
	}
	cmd := exec.Command(p.kubectl, "--context", p.ctx, "-n", p.ns, "port-forward",
		"--address", "127.0.0.1", p.target, ":"+strconv.Itoa(p.remote))
	cmd.Env = p.env
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	var stderr lockedBuf
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	ready := make(chan int, 1)
	go func() {
		sc := bufio.NewScanner(out)
		sent := false
		for sc.Scan() {
			if m := forwardingRe.FindStringSubmatch(sc.Text()); m != nil && !sent {
				n, _ := strconv.Atoi(m[1])
				ready <- n
				sent = true
			}
		}
	}()
	select {
	case n := <-ready:
		p.cmd, p.port, p.exited = cmd, n, exited
		p.logf("kubectl port-forward started (pid %d)", cmd.Process.Pid)
		return n, nil
	case <-exited:
		return 0, errors.New(lastLogLine(strings.TrimSpace(stderr.String())))
	case <-time.After(p.startWait):
		_ = cmd.Process.Kill()
		return 0, fmt.Errorf("kubectl port-forward did not start in %s", p.startWait)
	}
}

// restart drops kubectl so the next backend() starts a fresh one.
func (p *lazyPF) restart() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		_ = p.cmd.Process.Kill()
		p.cmd = nil
	}
}

func (p *lazyPF) stop() { p.restart() }

func (p *lazyPF) use(delta int) {
	p.mu.Lock()
	p.active += delta
	p.lastUse = time.Now()
	p.mu.Unlock()
}

// reap stops kubectl after idle without connections.
func (p *lazyPF) reap() {
	tick := p.tick
	if tick == 0 {
		tick = 15 * time.Second
	}
	for range time.Tick(tick) {
		p.mu.Lock()
		if p.cmd != nil && p.active == 0 && time.Since(p.lastUse) > p.idle {
			_ = p.cmd.Process.Kill()
			p.cmd = nil
			p.logf("idle for %s: kubectl stopped until the next connection", p.idle)
		}
		expired := p.expire > 0 && p.onExpire != nil && p.active == 0 && time.Since(p.lastUse) > p.expire
		p.mu.Unlock()
		if expired {
			p.logf("unused for %s: the forward goes away", p.expire)
			p.stop()
			p.onExpire()
			return
		}
	}
}

func (p *lazyPF) serve(l net.Listener) error {
	go p.reap()
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go p.handle(c)
	}
}

func (p *lazyPF) handle(c net.Conn) {
	defer c.Close()
	p.use(1)
	defer p.use(-1)
	var up net.Conn
	var err error
	for try := 0; try < 2; try++ { // a dead kubectl is restarted once
		var port int
		if port, err = p.backend(); err != nil {
			break
		}
		if up, err = net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 5*time.Second); err == nil {
			break
		}
		p.restart()
	}
	if err != nil {
		p.logf("connection failed: %v", err)
		if p.kind.web() {
			msg := fmt.Sprintf("rex8s: could not reach %s:%d in %s (%s)\n\n%v\n", p.target, p.remote, p.ns, p.ctx, err)
			fmt.Fprintf(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(msg), msg)
		}
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); closeWrite(up); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); closeWrite(c); done <- struct{}{} }()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	if t, ok := c.(*net.TCPConn); ok {
		_ = t.CloseWrite()
	}
}

type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// logTo returns a logf writing timestamped lines to w.
func logTo(w io.Writer) func(string, ...any) {
	var mu sync.Mutex
	return func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	}
}
