package forward

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
)

// Each background forward (pfbg) leaves a file in the registry directory
// (~/.cache/rex8s/pf/<pid>.json) while it runs, so `kpf -l` and the Raycast
// menu bar can list and stop them; its output goes to <local port>.log
// next to it.
//
// The file is written to a temporary name, locked (flock) and only then
// renamed into place, and the process keeps the lock until it exits. A
// file whose lock can be taken belongs to a process that is gone, even
// when another process got its PID since.

// Entry is a running forward.
type Entry struct {
	PID       int       `json:"pid"`
	Context   string    `json:"context"`
	Short     string    `json:"short"`
	Prod      bool      `json:"prod"`
	Namespace string    `json:"namespace"`
	Service   string    `json:"service"`
	Local     int       `json:"local"`
	Remote    int       `json:"remote"`
	Started   time.Time `json:"started"`
	Kind      Kind      `json:"kind,omitempty"`   // empty: http (entries from before kinds)
	Lazy      bool      `json:"lazy,omitempty"`   // kubectl runs only while in use
	Locked    bool      `json:"locked,omitempty"` // the process holds a lock on the file
}

// URL is how to reach the forward.
func (e Entry) URL() string {
	if e.Kind == "" {
		return KindHTTP.URL(e.Local)
	}
	return e.Kind.URL(e.Local)
}

// Registry is the directory of the running forwards.
type Registry struct {
	Dir string // ~/.cache/rex8s/pf
	// Bin is the rex8s binary: Ensure runs `Bin pfbg …` to hold a forward.
	Bin string
}

// unreadableAge: a registry file that can't be read is only removed once it
// is this old (it may be one being written by an older rex8s).
const unreadableAge = 5 * time.Second

// orphanAge: a log or temporary file with no running forward is removed
// once it is this old.
const orphanAge = time.Minute

func (r Registry) path(pid int) string { return filepath.Join(r.Dir, strconv.Itoa(pid)+".json") }

// LogPath is where the forward on local writes its log.
func (r Registry) LogPath(local int) string {
	return filepath.Join(r.Dir, strconv.Itoa(local)+".log")
}

// Register records e (a forward run by this process) until remove is
// called or the process exits.
func (r Registry) Register(e Entry) (remove func(), err error) {
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return func() {}, err
	}
	e.Locked = true
	data, err := json.Marshal(e)
	if err != nil {
		return func() {}, err
	}
	f, err := os.CreateTemp(r.Dir, ".pf-*.tmp")
	if err != nil {
		return func() {}, err
	}
	fail := func(err error) (func(), error) {
		f.Close()
		os.Remove(f.Name())
		return func() {}, err
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(err)
	}
	path := r.path(e.PID)
	if err := os.Rename(f.Name(), path); err != nil {
		return fail(err)
	}
	// f stays open, holding the lock, until remove (or the process exits)
	return func() {
		os.Remove(path)
		f.Close()
	}, nil
}

// held reports whether a process holds the lock on path.
func held(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// Alive reports whether a process with pid exists.
func Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// running: e's process is the one that registered it.
func running(path string, e Entry) bool {
	if e.PID <= 0 || !Alive(e.PID) {
		return false
	}
	if e.Locked {
		return held(path)
	}
	return true // an entry from before the locks: the PID is all there is
}

// List returns the running forwards, oldest first. Files of processes that
// are gone are removed.
func (r Registry) List() []Entry {
	files, _ := filepath.Glob(filepath.Join(r.Dir, "*.json"))
	out := []Entry{}
	for _, f := range files {
		var e Entry
		data, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(data, &e) != nil {
			if st, err := os.Stat(f); err == nil && time.Since(st.ModTime()) > unreadableAge {
				os.Remove(f)
			}
			continue
		}
		if !running(f, e) {
			os.Remove(f)
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

// GC removes what forwards that are gone left behind: their registry files
// (as List does), logs of no running forward and temporary files, and
// returns the names removed.
func (r Registry) GC() []string {
	before, _ := filepath.Glob(filepath.Join(r.Dir, "*.json"))
	live := map[string]bool{}
	ports := map[int]bool{}
	for _, e := range r.List() {
		live[r.path(e.PID)] = true
		ports[e.Local] = true
	}
	var removed []string
	for _, f := range before {
		if !live[f] {
			if _, err := os.Stat(f); os.IsNotExist(err) {
				removed = append(removed, filepath.Base(f))
			}
		}
	}
	old := func(f string) bool {
		st, err := os.Stat(f)
		return err == nil && time.Since(st.ModTime()) > orphanAge
	}
	logs, _ := filepath.Glob(filepath.Join(r.Dir, "*.log"))
	for _, f := range logs {
		n, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(f), ".log"))
		if (err != nil || !ports[n]) && old(f) && os.Remove(f) == nil {
			removed = append(removed, filepath.Base(f))
		}
	}
	tmps, _ := filepath.Glob(filepath.Join(r.Dir, ".pf-*.tmp"))
	for _, f := range tmps {
		if old(f) && os.Remove(f) == nil {
			removed = append(removed, filepath.Base(f))
		}
	}
	return removed
}

// Stop ends the running forward with pid.
func (r Registry) Stop(pid int) error {
	for _, e := range r.List() {
		if e.PID == pid {
			return syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	return fmt.Errorf("no port-forward with pid %d", pid)
}

// Find returns the running forward of ctx/ns/svc:remote.
func (r Registry) Find(ctx, ns, svc string, remote int) (Entry, bool) {
	for _, e := range r.List() {
		if e.Context == ctx && e.Namespace == ns && e.Service == svc && e.Remote == remote {
			return e, true
		}
	}
	return Entry{}, false
}

// lock serializes Ensure between processes, so two of them don't start the
// same forward twice.
func (r Registry) lock() (unlock func(), err error) {
	if err := os.MkdirAll(r.Dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(r.Dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// EnsureWait is how long Ensure waits for a new forward to register.
var EnsureWait = 10 * time.Second

// ListenerFD is the descriptor a pfbg started by Ensure listens on; the
// environment variable ListenerEnv says so.
const (
	ListenerFD  = 3
	ListenerEnv = "REX8S_PF_FD"
)

// Ensure returns the background forward of svc:port in c, starting one
// when none runs: on wantLocal when it is free (0: the service's port),
// otherwise on a random port. reused says it was already running.
//
// The local port is taken here and handed to `Bin pfbg` already listening,
// so no other process can take it in between.
func (r Registry) Ensure(c kube.Context, ns, svc string, port kube.ServicePort, wantLocal int) (e Entry, reused bool, err error) {
	unlock, err := r.lock()
	if err != nil {
		return Entry{}, false, err
	}
	defer unlock()
	if e, ok := r.Find(c.Name, ns, svc, port.Port); ok {
		return e, true, nil
	}
	r.GC()
	if wantLocal == 0 {
		wantLocal = port.Port
	}
	ln, err := Listen(wantLocal)
	if err != nil {
		return Entry{}, false, err
	}
	local := ln.Addr().(*net.TCPAddr).Port
	lf, err := ln.(*net.TCPListener).File()
	ln.Close() // lf keeps the socket listening
	if err != nil {
		return Entry{}, false, err
	}
	defer lf.Close()

	logPath := r.LogPath(local)
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return Entry{}, false, err
	}
	defer logf.Close()
	cmd := exec.Command(r.Bin, "pfbg", c.Name, ns, svc, fmt.Sprintf("%d:%d", local, port.Port), string(DetectKind(port)))
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.ExtraFiles = []*os.File{lf} // fd 3
	cmd.Env = append(os.Environ(), ListenerEnv+"="+strconv.Itoa(ListenerFD))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	lf.Close() // the socket is pfbg's now
	if err != nil {
		return Entry{}, false, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }() // no zombie when this process lives on
	pid := cmd.Process.Pid
	deadline := time.Now().Add(EnsureWait)
	for time.Now().Before(deadline) { // registered once it serves
		for _, e := range r.List() {
			if e.PID == pid {
				return e, false, nil
			}
		}
		select {
		case <-exited:
			data, _ := os.ReadFile(logPath)
			return Entry{}, false, errors.New("forward did not start: " + lastLine(string(data)))
		case <-time.After(100 * time.Millisecond):
		}
	}
	_ = cmd.Process.Kill()
	return Entry{}, false, fmt.Errorf("forward did not start in %s (log: %s)", EnsureWait, logPath)
}

// Inherited returns the listener Ensure handed to this process, or listens
// on 127.0.0.1:local when there is none.
func Inherited(local int) (net.Listener, error) {
	if os.Getenv(ListenerEnv) == strconv.Itoa(ListenerFD) {
		os.Unsetenv(ListenerEnv) // not for kubectl
		f := os.NewFile(ListenerFD, "listener")
		l, err := net.FileListener(f)
		f.Close()
		if err == nil {
			return l, nil
		}
	}
	return net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(local))
}

// Listen listens on 127.0.0.1:want when it is free (and above 1024),
// otherwise on a random free port.
func Listen(want int) (net.Listener, error) {
	if want > 1024 {
		if l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(want)); err == nil {
			return l, nil
		}
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
