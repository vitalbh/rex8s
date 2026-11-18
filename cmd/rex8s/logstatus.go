package main

import (
	"sort"
	"sync"

	"github.com/vitalbh/rex8s/internal/logstream"
)

// logStatus is a log pane's line on the Rex tab: "following 4 pods" (idle),
// or the stream that follows none and why (error): "api: login expired ·
// run kauth use". It hears logstream.Follower.State.
type logStatus struct {
	mu       sync.Mutex
	tab      *tabStatus
	what     string         // "" or " · grep timeout"
	pods     map[string]int // by stream
	problems map[string]string
}

func newLogStatus(tab *tabStatus, grep string) *logStatus {
	l := &logStatus{tab: tab, pods: map[string]int{}, problems: map[string]string{}}
	if grep != "" {
		l.what = " · grep " + grep
	}
	return l
}

func (l *logStatus) update(s logstream.Spec, pods int, problem string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := s.Label
	if key == "" {
		key = s.String()
	}
	l.pods[key] = pods
	if problem != "" {
		l.problems[key] = problem
	} else {
		delete(l.problems, key)
	}
	state, msg := l.line()
	l.tab.Set(state, msg)
}

func (l *logStatus) line() (state, msg string) {
	total := 0
	for _, n := range l.pods {
		total += n
	}
	follow := "following " + plural(total, "pod") + l.what
	if len(l.problems) == 0 {
		return "idle", follow
	}
	keys := make([]string, 0, len(l.problems))
	for k := range l.problems {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	msg = keys[0] + ": " + l.problems[keys[0]]
	if len(keys) > 1 {
		msg += " (+" + plural(len(keys)-1, "stream") + ")"
	}
	if total > 0 {
		msg = follow + " · " + msg
	}
	return "error", msg
}
