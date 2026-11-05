package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/vitalbh/rex8s/internal/tui"
)

type helpEntry struct {
	group, cmd, usage, short string
	details                  []string
}

// helpEntries: every command a rex8s pane has, grouped. Keep in sync with
// scripts/wrappers.sh and the README.
var helpEntries = []helpEntry{
	{"pane", "kctx", "kctx [CTX]", "switch this pane's cluster", []string{"No argument: picker. Only this pane changes; other tabs and ~/.kube/config stay."}},
	{"pane", "kns", "kns [NS|all]", "switch this pane's namespace", []string{"No argument: picker. kns all: every namespace — kubectl get/describe/top", "add -A by themselves, commands on one object ask for -n."}},
	{"pane", "kauth", "kauth [CLUSTER]", "log in again when the credentials expired", []string{"Opens the login page and reads the pasted kubectl config commands (only those)."}},
	{"pane", "rex8s doctor", "rex8s doctor · setup · config · agents", "check and set up rex8s", []string{"doctor: checks the install, Rex, kubectl/flux/k9s/claude and the config.", "setup: (re)installs into Rex. config: settings. agents install: the skill for coding agents."}},
	{"pane", "ktint", "ktint [#hex|off] [--save]", "background color of production panes", []string{"No argument: presets, previewed live; enter saves to config.yaml.", "ktint #1f1430 tries a color in this pane; --save keeps it; ktint reset undoes."}},
	{"pods", "knet", "knet", "network toolbox pod (netshoot) in this namespace", []string{"curl, dig, nc, tcpdump… from inside the cluster. Production asks first; the pod is removed on exit."}},
	{"pods", "kfail", "kfail [-n] [FILTER]", "failing pods with events and last logs", []string{"All namespaces (-n: this one). Enter: logs, why, ask (kask), shell, describe, events."}},
	{"pods", "klogs", "klogs [-A] [-g RE] [-s DUR] [FILTER]", "follow a workload's or a pod's logs in a split", []string{"Workloads first: all their pods merged; new pods join after a rollout.", "klogs -A: workloads on any cluster (mark several), one stream.", "-g REGEX keeps matching lines (highlighted); -s 1h starts an hour back."}},
	{"pods", "kgrep", "kgrep [-A] [PATTERN]", "search the logs of a workload or pod", []string{"klogs with a filter: pick, then type what to look for (regex, case-insensitive).", "Keeps following: new matching lines keep coming. kgrep -A: several clusters."}},
	{"pods", "kexec", "kexec [FILTER]", "shell into a pod in a split", []string{"Production asks first."}},
	{"pods", "kpf", "kpf [-n] [FILTER]", "port-forward a service in a split", []string{"Every namespace, this pane's first (-n: only it). On a free local port.", "kpf -l lists the running ones."}},
	{"pods", "kpf -f", "kpf NAME · kpf -f", "favorite port-forwards: start and open the browser", []string{"From config.yaml forwards (name, cluster, namespace, service, port, path).", "When it already runs, only the browser opens."}},
	{"pods", "kpf -l", "kpf -l", "running port-forwards: open, copy, stop", []string{"Also in the Raycast Kube Context menu bar."}},
	{"pods", "kwhy", "kwhy [FILTER]", "ask Claude why a pod is unhealthy (one answer)", []string{"Describe (env values redacted), events and logs; Claude's tools are off."}},
	{"pods", "kask", "kask [FILTER] · cmd | kask", "talk to Claude about the cluster or a pod", []string{"First row: the whole cluster. Read-only kubectl runs without asking;", "changes ask for approval, and production also needs REX8S_FORCE=1.", "kask --dump only saves the context file."}},
	{"pods", "knode", "knode [FILTER]", "shell on a node (kubectl debug node) in a split", []string{"Node filesystem at /host. Production asks first; the debug pod is removed on exit."}},
	{"cluster", "kincident", "kincident [CLUSTER] · --report · --ask", "a Rex session laid out for an incident", []string{"kstatus -w, live warnings, recent changes (kwhat) and a shell on the cluster.", "--report: Markdown with the timeline, warnings and broken pods (postmortem).", "--ask: the same report, then a kask conversation about it."}},
	{"cluster", "kstatus", "kstatus [-w]", "the cluster's health on one screen", []string{"Nodes, pods, workloads, Flux, jobs, alerts, certificates, warnings. -w refreshes."}},
	{"cluster", "kdisk", "kdisk [-A] [-a] [FILTER]", "how full the volumes are (PVCs and node disks)", []string{"Real usage from each node's kubelet, fullest first; above 70% by default (-a: all).", "-A: every cluster."}},
	{"cluster", "kreq", "kreq [-n] [FILTER]", "requests and limits vs real usage", []string{"Near the memory limit (OOM risk), over-provisioned (cost), no requests.", "This pane's cluster; usage is a kubectl top snapshot (needs metrics-server)."}},
	{"cluster", "kwarn", "kwarn [-n]", "stream Warning events only", nil},
	{"cluster", "kjobs", "kjobs [-n] [STATE] [FILTER]", "Jobs and CronJobs with logs and recent runs", []string{"STATE: failed, running, done, suspended, cron. Enter: logs, run now, rerun, suspend/resume, delete."}},
	{"flux", "kgit", "kgit [FILTER]", "where a workload is defined in Git: the file to change", []string{"Follows the Flux labels: HelmRelease → Kustomization → GitRepository.", "With a local clone (this directory or gitops_repos in the config) it finds the file; enter opens it in the editor or on GitHub."}},
	{"flux", "kflux", "kflux", "Flux resources, failing first: reconcile, suspend…", nil},
	{"flux", "fdiff", "fdiff [-A] [FILTER]", "what this working tree would change on the cluster", []string{"Run inside the GitOps repo. -A: every cluster."}},
	{"flux", "fwatch", "fwatch [-A] [--now]", "follow HEAD until Flux applied it and it rolled out", []string{"Run inside the GitOps repo after a push. Progress in the tab header."}},
	{"clusters", "kwhat", "kwhat [DURATION] [FILTER]", "what changed on every cluster (default the last hour)", []string{"Rollouts with the image change, what Flux applied, Helm upgrades, new commits.", "e.g. kwhat 6h api. Flux's part comes from events, kept about an hour."}},
	{"clusters", "kmulti", "kmulti [GROUP] [-n NS|-A] [CMD...]", "a tab with one pane per cluster, side by side", []string{"Pick clusters (space), or a group from the config (groups: {prod: [dal, sp]}).", "With CMD each pane runs it first (kmulti get pods -n api). Palette: Clusters Side by Side…"}},
	{"clusters", "kall", "kall CMD...", "type CMD in every cluster pane of this tab", []string{"In a kmulti tab: kall get pods -n api runs on every cluster at once.", "Production panes still ask before a change."}},
	{"clusters", "kjunk", "kjunk [debug|pods|jobs|pv] [FILTER]", "leftovers on every cluster: mark and delete", []string{"Ended debug pods (knode, knet), Failed/Evicted pods over a day old,", "Jobs finished over a week ago (not CronJob's), Released PVs. Production asks y/N."}},
	{"clusters", "kcron", "kcron [DURATION] [FILTER]", "what CronJobs run next on every cluster (default 6h)", []string{"One timeline, with how the last run went. Frequent jobs are folded.", "e.g. kcron 24h backup. Suspended ones are left out."}},
	{"clusters", "kver", "kver [-d] [FILTER]", "which version runs on which cluster", []string{"-d: only the workloads whose versions differ."}},
	{"clusters", "kdiff", "kdiff [FILTER]", "a workload's spec on two clusters", []string{"Pick one row (same name elsewhere) or mark two with space."}},
	{"clusters", "krun", "krun [FILTER] [-- CMD]", "run a command in the pods of the workloads you mark", []string{"e.g. krun nginx -- nginx -s reload. Production asks once (y/N)."}},
	{"clusters", "kroll", "kroll [FILTER]", "restart the workloads you mark, follow the rollouts", []string{"Production asks once (y/N)."}},
}

var helpKeys = []struct{ keys, what string }{
	{"⇧⌘J", "new tab: pick a cluster → isolated shell"},
	{"⇧⌘K", "new tab: pick a cluster → k9s · shell · warnings"},
	{"⇧⌘M", "new tab: pick clusters → one pane each, side by side"},
	{"⌘⇧P", "palette: Kubernetes: Flux Monitor, Restore Cluster Tabs"},
}

// khelp: every rex8s command in a picker; enter runs the one under the cursor.
func (a *app) khelp(args []string) error {
	filter := strings.Join(args, " ")
	byKey := map[string]helpEntry{}
	cw := 0
	for _, e := range helpEntries {
		cw = max(cw, len(e.cmd))
	}
	var items []tui.Item
	for _, e := range helpEntries {
		byKey[e.cmd] = e
		items = append(items, tui.Item{Key: e.cmd, Label: fmt.Sprintf("%-*s", cw, e.cmd), Hint: fmt.Sprintf("%-9s %s", e.group, e.short)})
	}
	for _, k := range helpKeys {
		items = append(items, tui.Item{Key: "key:" + k.keys, Label: fmt.Sprintf("%-*s", cw, k.keys), Hint: fmt.Sprintf("%-9s %s", "keys", k.what), Dim: true})
	}
	it, err := tui.Run(tui.Options{
		Title: "rex8s · enter runs the command",
		Items: items,
		Query: filter,
		PreviewHead: func(it tui.Item) string {
			e, ok := byKey[it.Key]
			if !ok {
				return tui.Bold.Render(strings.TrimSpace(it.Label)) + "\n\n" + tui.Dim.Render("A Rex key binding (rex/rex8s.lua).")
			}
			var b strings.Builder
			b.WriteString(tui.Bold.Render(e.usage) + "\n\n" + e.short + "\n")
			for _, d := range e.details {
				b.WriteString("\n" + tui.Dim.Render(d))
			}
			return b.String()
		},
	})
	if err != nil || it == nil {
		return err
	}
	e, ok := byKey[it.Key]
	if !ok {
		fmt.Println(strings.TrimSpace(it.Label) + " is a key binding: press it in Rex.")
		return nil
	}
	argv := strings.Fields(e.cmd)
	bin := filepath.Join(a.binDir, argv[0])
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("%s not installed (make install)", argv[0])
	}
	fmt.Println(tui.Dim.Render("$ " + e.cmd))
	return syscall.Exec(bin, argv, os.Environ())
}
