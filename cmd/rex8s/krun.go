package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// krun runs a command in every pod of the workloads you pick, on any cluster:
//
//	krun                         picker with every workload of every cluster
//	krun nginx                   the same, filtered by "nginx"
//	krun nginx -- nginx -s reload
//
// Workloads are Deployments, StatefulSets and DaemonSets, all namespaces; the
// command is asked for after the pick when not given. Production clusters ask
// for confirmation once for the whole run.
func (a *app) krun(args []string) error {
	filter, container, command, err := parseKrunArgs(args)
	if err != nil {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	cl.Timeout = 30 * time.Second
	raw, err := kube.Load(a.src)
	if err != nil {
		return err
	}
	clusters := fleet.All(raw, a.cfg)

	var skipped []string
	targets, _, err := a.pickTargets(cl, clusters, filter, "Run in which workloads?", &skipped)
	for _, s := range skipped {
		fmt.Fprintln(os.Stderr, tui.Warn.Render("  "+s))
	}
	if err != nil || len(targets) == 0 {
		return err
	}
	if len(command) == 0 {
		if command = askCommand(targets); len(command) == 0 {
			return nil
		}
	}
	pods, perTarget := runningPodsOf(cl, targets)
	if len(pods) == 0 {
		return errors.New("no running pods to run in")
	}

	// the plan, then confirmation for production
	cmdText := strings.Join(command, " ")
	fmt.Println(tui.Bold.Render(cmdText))
	touched := make([]kube.Context, 0, len(targets))
	for _, t := range targets {
		tag := ""
		if t.c.Prod {
			tag = " " + tui.Prod.Render(" PROD ")
		}
		touched = append(touched, t.c)
		fmt.Printf("  %-10s %s %s/%s · %d pod(s)%s\n", t.c.Short, kube.ShortKind(t.Kind), t.Namespace, t.Name, perTarget[t.key()], tag)
	}
	if ok, err := confirmChange(touched, fmt.Sprintf("krun: run %q in %d pod(s)", cmdText, len(pods))); !ok {
		return err
	}
	return printRunResults(pods, execInPods(cl, pods, container, command))
}

// podRef is one pod of a marked workload.
type podRef struct {
	t   target
	pod string
}

// runResult is what a command printed in one pod.
type runResult struct {
	out string
	err error
	dur time.Duration
}

// runningPodsOf: the running pods of each marked workload, and how many
// each one has (by target key). A workload whose pods can't be listed is
// reported and left out.
func runningPodsOf(cl kube.Client, targets []target) ([]podRef, map[string]int) {
	var pods []podRef
	perTarget := map[string]int{}
	for _, t := range targets {
		names, err := cl.RunningPods(t.c.Name, t.Namespace, t.Selector)
		if err != nil {
			fmt.Fprintln(os.Stderr, tui.Err.Render(fmt.Sprintf("  %s %s/%s: %v", t.c.Short, t.Namespace, t.Name, err)))
			continue
		}
		perTarget[t.key()] = len(names)
		for _, p := range names {
			pods = append(pods, podRef{t, p})
		}
	}
	return pods, perTarget
}

// execInPods runs command in every pod, at most 8 at a time; the results
// are in the order of pods.
func execInPods(cl kube.Client, pods []podRef, container string, command []string) []runResult {
	results := make([]runResult, len(pods))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, p := range pods {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			out, err := cl.Exec(ctx, p.t.c.Name, p.t.Namespace, p.pod, container, command)
			results[i] = runResult{out, err, time.Since(start)}
		}()
	}
	wg.Wait()
	return results
}

// printRunResults prints each pod's last lines of output in plan order,
// then the summary; it fails when any pod did.
func printRunResults(pods []podRef, results []runResult) error {
	failed := 0
	for i, p := range pods {
		r := results[i]
		status := tui.Ok.Render("ok  ")
		if r.err != nil {
			status = tui.Err.Render("FAIL")
			failed++
		}
		fmt.Printf("\n%s %-10s %s/%s %s\n", status, p.t.c.Short, p.t.Namespace, p.pod, tui.Dim.Render(r.dur.Round(10*time.Millisecond).String()))
		out := strings.TrimRight(r.out, "\n")
		if r.err != nil && out == "" {
			out = r.err.Error()
		}
		for _, line := range lastLines(out, 20) {
			fmt.Println("     " + line)
		}
	}
	summary := fmt.Sprintf("\n%d ok", len(pods)-failed)
	if failed > 0 {
		fmt.Println(summary + tui.Err.Render(fmt.Sprintf(" · %d failed", failed)))
		return fmt.Errorf("%d of %d failed", failed, len(pods))
	}
	fmt.Println(tui.Ok.Render(summary))
	return nil
}

// parseKrunArgs: krun [-c CONTAINER] [FILTER] [-- CMD...] ("--" optional
// when FILTER is given). Filter and command may be empty: the picker and a
// prompt ask for them.
func parseKrunArgs(args []string) (filter, container string, command []string, err error) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			command = args[i+1:]
			i = len(args)
		case a == "-c":
			if i+1 >= len(args) {
				return "", "", nil, errors.New("-c needs a container name")
			}
			container = args[i+1]
			i++
		case filter == "":
			filter = a
		default:
			command = args[i:]
			i = len(args)
		}
	}
	return filter, container, command, nil
}

// askCommand prompts for the command after the pick ("" cancels).
func askCommand(targets []target) []string {
	fmt.Fprintf(os.Stderr, "%s %s\n", tui.Bold.Render("Command to run in"), describeTargets(targets))
	line, _ := tui.AskLine(tui.Dim.Render("  e.g. nginx -s reload · empty cancels") + "\n  > ")
	return strings.Fields(line)
}

func describeTargets(ts []target) string {
	if len(ts) == 1 {
		t := ts[0]
		return fmt.Sprintf("%s %s/%s on %s", kube.ShortKind(t.Kind), t.Namespace, t.Name, t.c.Short)
	}
	seen := map[string]bool{}
	var cs []string
	for _, t := range ts {
		if !seen[t.c.Short] {
			seen[t.c.Short] = true
			cs = append(cs, t.c.Short)
		}
	}
	return fmt.Sprintf("%d workloads on %s", len(ts), strings.Join(cs, ", "))
}

// target is one workload on one cluster.
type target struct {
	c kube.Context
	kube.Workload
}

// workloadTargets parses `kubectl get deployments,statefulsets,daemonsets -o json`
// into targets without a cluster.
func workloadTargets(data []byte) ([]target, error) {
	ws, err := kube.ParseWorkloads(data)
	if err != nil {
		return nil, err
	}
	out := make([]target, len(ws))
	for i, w := range ws {
		out[i] = target{Workload: w}
	}
	return out, nil
}

func (t target) key() string { return t.c.Name + "|" + t.Kind + "/" + t.Namespace + "/" + t.Name }

// findWorkloads lists, on every cluster, the workloads whose name contains
// filter. Names and namespaces may differ between clusters, so each cluster's
// match is its own target; the user marks which ones to run in.
func (a *app) findWorkloads(cl kube.Client, clusters []kube.Context, filter string) ([]target, []string) {
	res := fleet.Read(a.readerFor(cl), clusters, [][]string{{"get", "deployments,statefulsets,daemonsets", "-A", "-o", "json"}},
		func(c kube.Context, out [][]byte) ([]target, error) {
			ts, err := workloadTargets(out[0])
			for i := range ts {
				ts[i].c = c
			}
			return ts, err
		})
	var found []target
	var skipped []string
	for _, s := range res.Skipped {
		skipped = append(skipped, s.Cluster.Short+": skipped, "+s.Reason)
	}
	f := strings.ToLower(filter)
	for _, r := range res.Rows {
		if f == "" || strings.Contains(strings.ToLower(r.V.Name), f) {
			found = append(found, r.V)
		}
	}
	// exact name first, then by name and cluster, so the same workload on
	// different clusters sits together
	sort.SliceStable(found, func(i, j int) bool {
		ei, ej := strings.EqualFold(found[i].Name, filter), strings.EqualFold(found[j].Name, filter)
		if ei != ej {
			return ei
		}
		if found[i].Name != found[j].Name {
			return found[i].Name < found[j].Name
		}
		return found[i].c.Short < found[j].c.Short
	})
	return found, skipped
}

// pickTargets: the picker opens at once and loads every workload of every
// cluster (one row per workload per cluster: names may differ between
// clusters), filtered by filter, which the user can edit. Marked rows (or the
// one under the cursor) are returned. With a filter that matches exactly one
// workload, no picker. all is every workload loaded (for callers that look
// for the same workload on other clusters).
func (a *app) pickTargets(cl kube.Client, clusters []kube.Context, filter, title string, skipped *[]string) (chosen, all []target, err error) {
	byKey := map[string]target{}
	load := func() ([]tui.Item, error) {
		var sk []string
		all, sk = a.findWorkloads(cl, clusters, "")
		*skipped = sk
		if len(all) == 0 {
			return nil, errors.New("no deployments, statefulsets or daemonsets found")
		}
		cw, ww := 0, 0
		for _, t := range all {
			cw = max(cw, len(t.c.Short))
			ww = max(ww, len(t.Namespace)+1+len(t.Name))
		}
		items := make([]tui.Item, 0, len(all))
		for _, t := range all {
			byKey[t.key()] = t
			badge := ""
			if t.c.Prod {
				badge = "PROD"
			}
			items = append(items, tui.Item{Key: t.key(), Badge: badge,
				Label: fmt.Sprintf("%-*s  %-6s %-*s", cw, t.c.Short, kube.ShortKind(t.Kind), ww, t.Namespace+"/"+t.Name),
				Hint:  t.c.Name})
		}
		return items, nil
	}

	if filter != "" {
		// one exact-ish match: straight to the command
		fmt.Fprintln(os.Stderr, tui.Dim.Render(fmt.Sprintf("looking for %q in %d clusters…", filter, len(clusters))))
		items, err := load()
		if err != nil {
			return nil, nil, err
		}
		var hits []target
		for _, t := range all {
			if strings.Contains(strings.ToLower(t.Name), strings.ToLower(filter)) {
				hits = append(hits, t)
			}
		}
		if len(hits) == 1 {
			return hits, all, nil
		}
		chosen, err = runTargetPicker(tui.Options{Title: title, Items: items, Query: filter}, byKey)
		return chosen, all, err
	}
	chosen, err = runTargetPicker(tui.Options{Title: fmt.Sprintf("%s (%d clusters)", title, len(clusters)), Load: load}, byKey)
	return chosen, all, err
}

func runTargetPicker(o tui.Options, byKey map[string]target) ([]target, error) {
	its, err := tui.RunMulti(o)
	if err != nil {
		return nil, err
	}
	out := make([]target, 0, len(its))
	for _, it := range its {
		out = append(out, byKey[it.Key])
	}
	return out, nil
}

func lastLines(s string, n int) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		return append([]string{fmt.Sprintf("… %d lines above", len(lines)-n)}, lines[len(lines)-n:]...)
	}
	return lines
}
