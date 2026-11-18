package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// The argument parsers and renderers split out of klogs, kcron and kjunk.

func TestParseKlogsArgs(t *testing.T) {
	filter, all, lo, err := parseKlogsArgs([]string{"-A", "-g", "time(out|d)", "--since", "1h", "api"})
	if err != nil || filter != "api" || !all || lo.Grep != "time(out|d)" || lo.Since != "1h" {
		t.Errorf("got %q %v %+v %v", filter, all, lo, err)
	}
	if _, _, _, err := parseKlogsArgs([]string{"-g", "("}); err == nil {
		t.Error("a bad regex must fail before the picker")
	}
}

func TestLogChoicesItems(t *testing.T) {
	c := kube.Context{Name: "use", Short: "use", Namespace: "api"}
	lc := newLogChoices(kube.Client{}, c)
	if lc.where != "use/api" || !slices.Equal(lc.scope, []string{"-n", "api"}) {
		t.Fatalf("scope %v where %q", lc.scope, lc.where)
	}
	ws := []target{
		{Workload: kube.Workload{Kind: "Deployment", Namespace: "api", Name: "web", Selector: "app=web"}},
		{Workload: kube.Workload{Kind: "Deployment", Namespace: "api", Name: "noselector"}},
	}
	ps := []kube.Pod{
		{Namespace: "api", Name: "web-1", Phase: "Running", Ready: 1, Total: 1, Labels: map[string]string{"app": "web"}},
		{Namespace: "api", Name: "web-2", Phase: "Pending", Total: 1, Labels: map[string]string{"app": "web"}},
	}
	items := lc.items(ws, ps)
	if len(items) != 3 {
		t.Fatalf("want the workload with a selector and both pods, got %d", len(items))
	}
	if items[0].Badge != "ALL" || !strings.Contains(items[0].Hint, "all 2 pod(s)") {
		t.Errorf("workload row: %+v", items[0])
	}
	if _, ok := lc.works[items[0].Key]; !ok {
		t.Error("workload not recorded")
	}
	if items[1].Key != "web-2" { // waiting before ok
		t.Errorf("pods out of order: %q, %q", items[1].Key, items[2].Key)
	}

	all := newLogChoices(kube.Client{}, kube.Context{Short: "use", AllNS: true})
	if all.where != "use (all namespaces)" || all.name("api", "web-1") != "api/web-1" {
		t.Errorf("all namespaces: where %q name %q", all.where, all.name("api", "web-1"))
	}
}

func TestParseKcronArgs(t *testing.T) {
	if w, f := parseKcronArgs(nil); w != 6*time.Hour || f != "" {
		t.Errorf("default: %v %q", w, f)
	}
	if w, f := parseKcronArgs([]string{"24h", "backup"}); w != 24*time.Hour || f != "backup" {
		t.Errorf("got %v %q", w, f)
	}
}

func TestRenderCronRuns(t *testing.T) {
	c := kube.Context{Name: "use", Short: "use"}
	cj := kube.CronJob{Namespace: "ops", Name: "sync", Schedule: "*/5 * * * *"}
	at := time.Date(2026, 3, 2, 10, 0, 0, 0, time.Local)
	var runs []cronRun
	for i := range 5 {
		runs = append(runs, cronRun{at: at.Add(time.Duration(i) * 5 * time.Minute), c: c, cj: cj})
	}
	out := tui.Clean(renderCronRuns(runs, time.Hour, 1, 0))
	if n := strings.Count(out, "ops/sync"); n != 3 {
		t.Errorf("folded after 3 runs, got %d lines:\n%s", n, out)
	}
	for _, want := range []string{"(+2 more runs)", "5 run(s) in the next", "1 suspended not shown"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if out := tui.Clean(renderCronRuns(nil, time.Hour, 0, 0)); !strings.Contains(out, "nothing scheduled") {
		t.Errorf("no runs:\n%s", out)
	}
}

func TestParseKjunkArgs(t *testing.T) {
	cats, filter := parseKjunkArgs([]string{"pods", "JOBS", "api"})
	if !cats["pod"] || !cats["job"] || len(cats) != 2 || filter != "api" {
		t.Errorf("got %v %q", cats, filter)
	}
	if got := kjunkTitle(cats); !strings.HasSuffix(got, " · job, pod") {
		t.Errorf("title %q", got)
	}
}
