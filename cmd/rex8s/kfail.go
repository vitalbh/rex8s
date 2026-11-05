package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

const failLogLines = 30

// kfail: only the unhealthy pods of the cluster (all namespaces; -n this
// pane's namespace), with their recent events and last log lines in the
// preview — the previous container's logs when it crashed. Logs are fetched
// only for the pod under the cursor. Enter offers logs / why / shell /
// describe / events.
//
// --context CTX picks the cluster instead of this pane's (Raycast opens kfail
// in a tab of its own that way); --wait keeps describe/events output on
// screen until enter.
func (a *app) kfail(args []string) error {
	allNS, filter, ctxName, wait := true, "", "", false
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-n":
			allNS = false
		case arg == "--wait":
			wait = true
		case arg == "--context" && i+1 < len(args):
			ctxName = args[i+1]
			i++
		default:
			filter = arg
		}
	}
	var c kube.Context
	var err error
	if ctxName != "" {
		c, err = a.context(ctxName)
	} else {
		c, err = kube.Current(a.cfg, "", "")
	}
	if err != nil {
		return err
	}
	pause := func() {
		if wait {
			fmt.Fprint(os.Stderr, tui.Dim.Render("\npress enter to close"))
			bufio.NewReader(os.Stdin).ReadString('\n')
		}
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	ns := c.Namespace
	if ns == "" {
		ns = "default"
	}
	scope, where := []string{"-A"}, c.Short
	if !allNS {
		scope, where = []string{"-n", ns}, c.Short+"/"+ns
	}

	pods := map[string]pod{}
	it, err := tui.Run(tui.Options{
		Title: "Failing pods on " + where,
		Query: filter,
		Load: func() ([]tui.Item, error) {
			out, err := cl.Get(c.Name, append([]string{"get", "pods", "-o", "json"}, scope...)...)
			if err != nil {
				return nil, err
			}
			ps, err := parsePods(out)
			if err != nil {
				return nil, err
			}
			ps = failingPods(ps)
			if len(ps) == 0 {
				return nil, fmt.Errorf("no failing pods on %s", where)
			}
			w := 0
			for _, p := range ps {
				w = max(w, len(p.Namespace)+1+len(p.Name))
			}
			items := make([]tui.Item, 0, len(ps))
			for _, p := range ps {
				key := p.Namespace + "/" + p.Name
				pods[key] = p
				items = append(items, tui.Item{Key: key, Label: fmt.Sprintf("%-*s", w, key), Hint: failHint(p), Badge: failBadge(p)})
			}
			return items, nil
		},
		PreviewHead: func(it tui.Item) string { return failHead(pods[it.Key]) },
		PreviewBody: func(it tui.Item) string { return failBody(cl, c.Name, pods[it.Key]) },
	})
	if err != nil || it == nil {
		return err
	}
	p := pods[it.Key]

	actions := []tui.Item{
		{Key: "logs", Label: fmt.Sprintf("%-9s", "logs"), Hint: "follow the logs in a split"},
		{Key: "why", Label: fmt.Sprintf("%-9s", "why"), Hint: "ask Claude why it is unhealthy (kwhy)"},
		{Key: "shell", Label: fmt.Sprintf("%-9s", "shell"), Hint: "open a shell in it in a split (kexec)"},
		{Key: "describe", Label: fmt.Sprintf("%-9s", "describe"), Hint: "kubectl describe"},
		{Key: "events", Label: fmt.Sprintf("%-9s", "events"), Hint: "all events of the pod"},
	}
	act, err := tui.Run(tui.Options{Title: fmt.Sprintf("%s/%s on %s", p.Namespace, p.Name, c.Short), Items: actions})
	if err != nil || act == nil {
		return err
	}
	self := filepath.Join(a.binDir, "rex8s")
	inRex := rex.Block() != ""
	switch act.Key {
	case "logs", "shell":
		container, ok, err := pickContainer(p, act.Key == "logs")
		if err != nil || !ok {
			return err
		}
		verb, tail := "logtail", a.logtail
		if act.Key == "shell" {
			verb, tail = "exectail", a.exectail
		}
		if !inRex {
			return tail(c.Name, p.Namespace, p.Name, container)
		}
		argv := []string{self, verb, c.Name, p.Namespace, p.Name}
		if container != "" {
			argv = append(argv, container)
		}
		label := "logs "
		if act.Key == "shell" {
			label = "exec "
		}
		return rex.Split(label+p.Name, argv...)
	case "why":
		if _, err := a.aiCommand(); err != nil {
			return err
		}
		if !inRex {
			return a.whytail(c.Name, p.Namespace, p.Name)
		}
		return rex.Split("why "+p.Name, self, "whytail", c.Name, p.Namespace, p.Name)
	case "describe":
		defer pause()
		return a.runKubectl(false, "--context", c.Name, "-n", p.Namespace, "describe", "pod", p.Name)
	case "events":
		fmt.Println(objectEvents(cl, c.Name, p.Namespace, "Pod", p.Name, 0))
		pause()
	}
	return nil
}

// failingPods keeps the pods that need attention, worst first: failing,
// then not ready, then running but restarted because they were killed.
func failingPods(ps []pod) []pod {
	var out []pod
	for _, p := range ps {
		if failRank(p) < 3 {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := failRank(out[i]), failRank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i].Restarts > out[j].Restarts
	})
	return out
}

func failRank(p pod) int {
	switch p.badge() {
	case "FAIL":
		return 0
	case "WAIT":
		return 1
	case "ok":
		if p.Restarts > 0 && (p.LastReason == "OOMKilled" || p.LastReason == "Error") {
			return 2
		}
	}
	return 3
}

func failBadge(p pod) string {
	if b := p.badge(); b != "ok" {
		return b
	}
	return "WAIT"
}

func failHint(p pod) string {
	h := fmt.Sprintf("%-18s %d/%d  restarts %-3d %s", p.status(), p.Ready, p.Total, p.Restarts, age(p.Created))
	if p.LastReason != "" && p.LastReason != p.status() {
		h += "  last: " + p.LastReason
	}
	return h
}

func failHead(p pod) string {
	lines := []string{
		tui.Bold.Render(p.Name) + "  " + tui.Warn.Render(p.status()),
		"",
		tui.Dim.Render("namespace ") + p.Namespace,
		tui.Dim.Render("ready     ") + fmt.Sprintf("%d/%d", p.Ready, p.Total),
		tui.Dim.Render("restarts  ") + fmt.Sprint(p.Restarts),
		tui.Dim.Render("age       ") + age(p.Created),
	}
	if p.LastReason != "" {
		lines = append(lines, tui.Dim.Render("last exit ")+p.LastReason)
	}
	return strings.Join(lines, "\n")
}

// failBody: the pod's last events, then its last log lines. When it restarted,
// the previous container's logs come first: that is where the crash is.
func failBody(cl kube.Client, ctxName string, p pod) string {
	parts := []string{objectEvents(cl, ctxName, p.Namespace, "Pod", p.Name, 4)}
	logArgs := []string{"logs", "-n", p.Namespace, p.Name, fmt.Sprintf("--tail=%d", failLogLines)}
	if len(p.Containers) > 1 {
		logArgs = append(logArgs, "--all-containers", "--prefix")
	}
	if p.Restarts > 0 {
		if out, err := cl.Get(ctxName, append(logArgs, "--previous")...); err == nil && strings.TrimSpace(string(out)) != "" {
			parts = append(parts, tui.Bold.Render("previous container (before the last restart)"), logTail(string(out)))
			return strings.Join(parts, "\n\n")
		}
	}
	out, err := cl.Get(ctxName, logArgs...)
	switch {
	case err != nil:
		parts = append(parts, tui.Dim.Render("no logs: "+lastLogLine(err.Error())))
	case strings.TrimSpace(string(out)) == "":
		parts = append(parts, tui.Dim.Render("no logs yet"))
	default:
		parts = append(parts, tui.Bold.Render("logs"), logTail(string(out)))
	}
	return strings.Join(parts, "\n\n")
}

// logTail keeps the last failLogLines lines, each cut to a sane width.
func logTail(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > failLogLines {
		lines = lines[len(lines)-failLogLines:]
	}
	for i, l := range lines {
		lines[i] = truncate(strings.ReplaceAll(l, "\t", "  "), 220)
	}
	return strings.Join(lines, "\n")
}

func lastLogLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
