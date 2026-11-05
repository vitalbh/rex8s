package main

import (
	"strings"
	"testing"
)

func TestSummarizeAlerts(t *testing.T) {
	lines := summarizeAlerts([]firingAlert{
		{Name: "KubePodCrashLooping", Severity: "warning"},
		{Name: "KubePodCrashLooping", Severity: "warning"},
		{Name: "NodeDown", Severity: "critical"},
		{Name: "Info", Severity: "info"},
	}, 2)
	got := stripANSI(strings.Join(lines, "\n"))
	want := "critical NodeDown \nwarning KubePodCrashLooping ×2 \n… 1 more"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPlural(t *testing.T) {
	if plural(1, "cronjob") != "1 cronjob" || plural(3, "cronjob") != "3 cronjobs" {
		t.Error(plural(1, "cronjob"), plural(3, "cronjob"))
	}
}
