package main

import (
	"strings"
	"testing"
)

func TestFailingPods(t *testing.T) {
	ps := []pod{
		{Name: "healthy", Phase: "Running", Ready: 1, Total: 1},
		{Name: "old-restarts", Phase: "Running", Ready: 1, Total: 1, Restarts: 3, LastReason: "Completed"},
		{Name: "oom", Phase: "Running", Ready: 1, Total: 1, Restarts: 2, LastReason: "OOMKilled"},
		{Name: "pending", Phase: "Pending", Total: 1},
		{Name: "crash", Phase: "Running", Reason: "CrashLoopBackOff", Total: 1, Restarts: 9},
		{Name: "job", Phase: "Succeeded", Total: 1},
	}
	got := failingPods(ps)
	want := []string{"crash", "pending", "oom"}
	if len(got) != len(want) {
		t.Fatalf("got %d pods, want %v", len(got), want)
	}
	for i, p := range got {
		if p.Name != want[i] {
			t.Errorf("pod %d = %s, want %s", i, p.Name, want[i])
		}
	}
}

func TestLogTail(t *testing.T) {
	s := ""
	for i := 0; i < 50; i++ {
		s += "line\n"
	}
	if n := len(strings.Split(logTail(s), "\n")); n != failLogLines {
		t.Errorf("logTail kept %d lines, want %d", n, failLogLines)
	}
}
