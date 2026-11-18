package forward

import (
	"bytes"
	"context"
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

	"github.com/vitalbh/rex8s/internal/kube"
)

// DefaultIdle is how long kubectl keeps running without a connection.
const DefaultIdle = 10 * time.Minute

// ErrStopped: the forward was stopped.
var ErrStopped = errors.New("port-forward stopped")

// Lazy holds a local port for a service or pod and runs `kubectl
// port-forward` only while someone uses it: the first connection starts
// kubectl (on a random port of its own), connections are copied to it, and
// after Idle without connections kubectl stops until the next one. When
// kubectl dies (the pod went away), the next connection starts it again.
//
// Set the fields, then call Serve; they must not change afterwards.
type Lazy struct {
	Client    kube.Client
	Context   string
	Namespace string
	Target    string // svc/NAME or pod/NAME
	Remote    int
	Kind      Kind
	Idle      time.Duration                    // 0: DefaultIdle
	StartWait time.Duration                    // how long kubectl may take to forward (0: 20s)
	Tick      time.Duration                    // how often idle is checked (0: 15s)
	Expire    time.Duration                    // no connection this long: OnExpire (0: never)
	OnExpire  func()                           // called once, after Stop
	Logf      func(format string, args ...any) // nil: discard

	mu       sync.Mutex
	cmd      *exec.Cmd
	port     int // kubectl's local port while it runs
	exited   chan struct{}
	starting *start // the start in progress, which other connections wait for
	active   int
	lastUse  time.Time
	stopped  bool
	done     chan struct{} // closed by Stop
	once     sync.Once
}

type start struct {
	done chan struct{}
	err  error
}

var forwardingRe = regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+) ->`)

func (p *Lazy) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

func (p *Lazy) init() {
	p.once.Do(func() {
		p.done = make(chan struct{})
		p.mu.Lock()
		if p.lastUse.IsZero() {
			p.lastUse = time.Now()
		}
		p.mu.Unlock()
	})
}

// backend returns kubectl's port, starting kubectl when it is not running.
// The lock is not held while kubectl starts: Stop stays immediate, and
// connections arriving meanwhile wait for the same start.
func (p *Lazy) backend() (int, error) {
	p.init()
	for {
		p.mu.Lock()
		if p.stopped {
			p.mu.Unlock()
			return 0, ErrStopped
		}
		if p.cmd != nil {
			select {
			case <-p.exited:
				p.cmd = nil
			default:
				port := p.port
				p.mu.Unlock()
				return port, nil
			}
		}
		if s := p.starting; s != nil {
			p.mu.Unlock()
			<-s.done
			if s.err != nil {
				return 0, s.err
			}
			continue
		}
		s := &start{done: make(chan struct{})}
		p.starting = s
		p.mu.Unlock()

		cmd, port, exited, err := p.launch()
		p.mu.Lock()
		p.starting = nil
		if err == nil && p.stopped {
			_ = cmd.Process.Kill()
			err = ErrStopped
		}
		if err == nil {
			p.cmd, p.port, p.exited = cmd, port, exited
		}
		s.err = err
		p.mu.Unlock()
		close(s.done)
		if err != nil {
			return 0, err
		}
		return port, nil
	}
}

// launch starts kubectl and waits until it forwards, exits, or StartWait
// passes. kubectl's stderr goes to Logf line by line; its last lines make
// the error when it exits before forwarding.
func (p *Lazy) launch() (*exec.Cmd, int, chan struct{}, error) {
	cmd := p.Client.Command(context.Background(), p.Context, "-n", p.Namespace, "port-forward",
		"--address", "127.0.0.1", p.Target, ":"+strconv.Itoa(p.Remote))
	ready := make(chan int, 1)
	cmd.Stdout = &lineWriter{line: func(l string) {
		if m := forwardingRe.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[1])
			select {
			case ready <- n:
			default:
			}
		}
	}}
	tail := &tailLines{max: 5}
	stderr := &lineWriter{line: func(l string) {
		tail.add(l)
		p.logf("kubectl: %s", l)
	}}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, 0, nil, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); stderr.Close(); close(exited) }()
	wait := p.StartWait
	if wait == 0 {
		wait = 20 * time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case n := <-ready:
		p.logf("kubectl port-forward started (pid %d)", cmd.Process.Pid)
		return cmd, n, exited, nil
	case <-exited:
		msg := tail.last()
		if msg == "" {
			msg = "kubectl port-forward exited"
		}
		return nil, 0, nil, errors.New(msg)
	case <-p.done:
		_ = cmd.Process.Kill()
		return nil, 0, nil, ErrStopped
	case <-timer.C:
		_ = cmd.Process.Kill()
		return nil, 0, nil, fmt.Errorf("kubectl port-forward did not start in %s", wait)
	}
}

// restart drops kubectl so the next backend() starts a fresh one.
func (p *Lazy) restart() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		_ = p.cmd.Process.Kill()
		p.cmd = nil
	}
}

// Stop kills kubectl and ends the idle checks; connections that come after
// fail. Serve keeps accepting until its listener is closed.
func (p *Lazy) Stop() {
	p.init()
	p.mu.Lock()
	if !p.stopped {
		p.stopped = true
		close(p.done)
	}
	if p.cmd != nil {
		_ = p.cmd.Process.Kill()
		p.cmd = nil
	}
	p.mu.Unlock()
}

// Running reports whether kubectl runs now.
func (p *Lazy) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil
}

// LastUse is when a connection last came or went.
func (p *Lazy) LastUse() time.Time {
	p.init()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastUse
}

func (p *Lazy) use(delta int) {
	p.mu.Lock()
	p.active += delta
	p.lastUse = time.Now()
	p.mu.Unlock()
}

// reap stops kubectl after Idle without connections, and the whole forward
// after Expire.
func (p *Lazy) reap() {
	tick, idle := p.Tick, p.Idle
	if tick == 0 {
		tick = 15 * time.Second
	}
	if idle == 0 {
		idle = DefaultIdle
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-t.C:
		}
		p.mu.Lock()
		if p.cmd != nil && p.active == 0 && time.Since(p.lastUse) > idle {
			_ = p.cmd.Process.Kill()
			p.cmd = nil
			p.logf("idle for %s: kubectl stopped until the next connection", idle)
		}
		expired := p.Expire > 0 && p.OnExpire != nil && p.active == 0 && time.Since(p.lastUse) > p.Expire
		p.mu.Unlock()
		if expired {
			p.logf("unused for %s: the forward goes away", p.Expire)
			p.Stop()
			p.OnExpire()
			return
		}
	}
}

// Serve accepts connections on l until l is closed.
func (p *Lazy) Serve(l net.Listener) error {
	p.init()
	go p.reap()
	for {
		c, err := l.Accept()
		if err != nil {
			return err
		}
		go p.handle(c)
	}
}

func (p *Lazy) handle(c net.Conn) {
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
		if p.Kind.Web() {
			msg := fmt.Sprintf("rex8s: could not reach %s:%d in %s (%s)\n\n%v\n", p.Target, p.Remote, p.Namespace, p.Context, err)
			fmt.Fprintf(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(msg), msg)
		}
		return
	}
	pipe(c, up)
}

// pipe copies between the client c and kubectl's side up until the
// connection is over, then closes both. It is over when kubectl's side ends
// (the answer is complete, or kubectl died) or either copy fails; a client
// that only closes its sending side (EOF) still gets the rest of the answer.
// So a client that keeps its side open never holds a dead forward busy.
func pipe(c, up net.Conn) {
	var once sync.Once
	closeBoth := func() { once.Do(func() { c.Close(); up.Close() }) }
	defer closeBoth()
	done := make(chan struct{}, 2)
	go func() {
		if _, err := io.Copy(up, c); err != nil {
			closeBoth()
		} else {
			closeWrite(up)
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(c, up)
		closeBoth()
		done <- struct{}{}
	}()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	if t, ok := c.(*net.TCPConn); ok {
		_ = t.CloseWrite()
	}
}

// lineWriter calls line for each line written to it. A line longer than
// maxLine is cut, so a process that never writes a newline can't grow it.
type lineWriter struct {
	mu   sync.Mutex
	buf  []byte
	line func(string)
}

const maxLine = 4096

func (w *lineWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(b)
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			room := maxLine - len(w.buf)
			if len(b) > room {
				w.buf = append(w.buf, b[:room]...)
				w.flush()
				b = b[room:]
				continue
			}
			w.buf = append(w.buf, b...)
			break
		}
		w.buf = append(w.buf, b[:i]...)
		if len(w.buf) > maxLine {
			w.buf = w.buf[:maxLine]
		}
		w.flush()
		b = b[i+1:]
	}
	return n, nil
}

// Close passes on the last line when it had no newline.
func (w *lineWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flush()
	return nil
}

func (w *lineWriter) flush() {
	if l := strings.TrimSpace(string(w.buf)); l != "" {
		w.line(l)
	}
	w.buf = w.buf[:0]
}

// tailLines keeps the last max lines.
type tailLines struct {
	mu    sync.Mutex
	max   int
	lines []string
}

func (t *tailLines) add(l string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, l)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
}

func (t *tailLines) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.lines) == 0 {
		return ""
	}
	return t.lines[len(t.lines)-1]
}

// LogTo returns a Logf writing timestamped lines to w.
func LogTo(w io.Writer) func(string, ...any) {
	var mu sync.Mutex
	return func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	}
}
