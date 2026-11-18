package main

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/vitalbh/rex8s/internal/rex"
)

// tabStatus is this pane's state on the Rex tab (OSC 7501, the root
// record): one short line, written only when it changes, and cleared when
// the command ends — by return (Clear) or by ctrl+c / the pane closing.
// Outside Rex it does nothing.
type tabStatus struct {
	mu   sync.Mutex
	last string
	send func(state, kind, msg string) // rex.StatusKind on the root record; tests swap it
	stop func()
}

// newTabStatus starts reporting. Call Clear when the command returns.
func newTabStatus() *tabStatus {
	s := &tabStatus{send: func(state, kind, msg string) { rex.StatusKind("", state, kind, "", msg) }, stop: func() {}}
	if rex.Block() == "" {
		s.send = func(string, string, string) {}
		return s
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	var once sync.Once
	s.stop = func() { once.Do(func() { signal.Stop(sig); close(done) }) }
	go func() {
		select {
		case got := <-sig:
			s.Clear()
			// die of the signal as before: default handling, then raise it again
			signal.Stop(sig)
			if p, err := os.FindProcess(os.Getpid()); err == nil {
				_ = p.Signal(got)
			}
		case <-done:
		}
	}()
	return s
}

// Set reports state (idle, working, done, error) with msg.
func (s *tabStatus) Set(state, msg string) { s.set(state, "", msg) }

// Blocked reports that the user must do something; kind is permission,
// question or auth (a login expired).
func (s *tabStatus) Blocked(kind, msg string) { s.set("blocked", kind, msg) }

func (s *tabStatus) set(state, kind, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := state + "\x00" + kind + "\x00" + msg
	if key == s.last {
		return
	}
	s.last = key
	s.send(state, kind, msg)
}

// Clear removes the status (the command ended).
func (s *tabStatus) Clear() {
	s.mu.Lock()
	if s.last != "" {
		s.last = ""
		s.send("clear", "", "")
	}
	s.mu.Unlock()
	s.stop()
}
