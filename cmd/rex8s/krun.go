package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

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
	k, err := a.kubectl()
	if err != nil {
		return err
	}
	raw, err := kube.Load(a.src)
	if err != nil {
		return err
	}
	clusters := uniqueClusters(kube.Contexts(raw, a.cfg))

	var skipped []string
	targets, err := a.pickTargets(k, clusters, filter, &skipped)
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

	// the pods of each marked workload
	type podRef struct {
		t   target
		pod string
	}
	var pods []podRef
	perTarget := map[string]int{}
	for _, t := range targets {
		names, err := runningPods(k, a.src, t.c.Name, t.Namespace, t.Selector)
		if err != nil {
			fmt.Fprintln(os.Stderr, tui.Err.Render(fmt.Sprintf("  %s %s/%s: %v", t.c.Short, t.Namespace, t.Name, err)))
			continue
		}
		perTarget[t.key()] = len(names)
		for _, p := range names {
			pods = append(pods, podRef{t, p})
		}
	}
	if len(pods) == 0 {
		return errors.New("no running pods to run in")
	}

	// the plan, then confirmation for production
	cmdText := strings.Join(command, " ")
	fmt.Println(tui.Bold.Render(cmdText))
	var prod []string
	seenProd := map[string]bool{}
	for _, t := range targets {
		tag := ""
		if t.c.Prod {
			tag = " " + tui.Prod.Render(" PROD ")
			if !seenProd[t.c.Short] {
				seenProd[t.c.Short] = true
				prod = append(prod, t.c.Short)
			}
		}
		fmt.Printf("  %-10s %s %s/%s · %d pod(s)%s\n", t.c.Short, kube.ShortKind(t.Kind), t.Namespace, t.Name, perTarget[t.key()], tag)
	}
	sort.Strings(prod)
	if len(prod) > 0 && os.Getenv("REX8S_FORCE") == "" && !confirmRun(cmdText, len(pods), prod) {
		fmt.Fprintln(os.Stderr, "cancelled.")
		return nil
	}

	// run, at most 8 at a time; print in plan order
	type result struct {
		out string
		err error
		dur time.Duration
	}
	results := make([]result, len(pods))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, p := range pods {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			out, err := execInPod(k, a.src, p.t.c.Name, p.t.Namespace, p.pod, container, command)
			results[i] = result{out, err, time.Since(start)}
		}()
	}
	wg.Wait()

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

// confirmRun: a y/N question, for runs that touch production (the plan with
// every workload and pod count is printed right above it).
func confirmRun(cmdText string, pods int, prod []string) bool {
	fmt.Fprintf(os.Stderr, "\n%s %s %s %s",
		tui.Prod.Render(" PROD "),
		fmt.Sprintf("Run %q in %d pod(s) on", cmdText, pods),
		tui.Bold.Render(strings.Join(prod, ", "))+"?",
		tui.Dim.Render("[y/N] "))
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	defer tty.Close()
	ans, _ := bufio.NewReader(tty).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(ans)) {
	case "y", "yes", "s", "sim":
		return true
	}
	return false
}

// askCommand prompts for the command after the pick ("" cancels).
func askCommand(targets []target) []string {
	fmt.Fprintf(os.Stderr, "%s %s\n", tui.Bold.Render("Command to run in"), describeTargets(targets))
	fmt.Fprint(os.Stderr, tui.Dim.Render("  e.g. nginx -s reload · empty cancels")+"\n  > ")
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return nil
	}
	defer tty.Close()
	line, _ := bufio.NewReader(tty).ReadString('\n')
	return strings.Fields(strings.TrimSpace(line))
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

// uniqueClusters drops aliases: contexts pointing at the same server.
func uniqueClusters(cs []kube.Context) []kube.Context {
	seen := map[string]bool{}
	var out []kube.Context
	for _, c := range cs {
		key := c.Server
		if key == "" {
			key = c.Name
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// target is one workload on one cluster.
type target struct {
	c                     kube.Context
	Kind, Namespace, Name string
	Selector              string
}

func (t target) key() string { return t.c.Name + "|" + t.Kind + "/" + t.Namespace + "/" + t.Name }

// findWorkloads lists, on every cluster, the workloads whose name contains
// filter. Names and namespaces may differ between clusters, so each cluster's
// match is its own target; the user marks which ones to run in.
func (a *app) findWorkloads(k string, clusters []kube.Context, filter string) ([]target, []string) {
	type res struct {
		c   kube.Context
		ws  []target
		err error
	}
	results := make([]res, len(clusters))
	var wg sync.WaitGroup
	for i, c := range clusters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := kubectlOut(k, a.src, 30*time.Second, "--context", c.Name, "get", "deployments,statefulsets,daemonsets", "-A", "-o", "json")
			if err != nil {
				results[i] = res{c: c, err: err}
				return
			}
			ws, err := parseWorkloadSelectors([]byte(out))
			results[i] = res{c: c, ws: ws, err: err}
		}()
	}
	wg.Wait()

	var found []target
	var skipped []string
	f := strings.ToLower(filter)
	for _, r := range results {
		if r.err != nil {
			why := lastLogLine(r.err.Error())
			if kube.AuthExpired(r.err) {
				why = "login expired (kauth " + r.c.Short + ")"
			}
			skipped = append(skipped, r.c.Short+": skipped, "+why)
			continue
		}
		for _, w := range r.ws {
			if f == "" || strings.Contains(strings.ToLower(w.Name), f) {
				w.c = r.c
				found = append(found, w)
			}
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

func parseWorkloadSelectors(data []byte) ([]target, error) {
	var list struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Selector struct {
					MatchLabels map[string]string `json:"matchLabels"`
				} `json:"selector"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]target, 0, len(list.Items))
	for _, it := range list.Items {
		var sel []string
		for k, v := range it.Spec.Selector.MatchLabels {
			sel = append(sel, k+"="+v)
		}
		sort.Strings(sel)
		out = append(out, target{Kind: it.Kind, Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, Selector: strings.Join(sel, ",")})
	}
	return out, nil
}

// pickTargets: the picker opens at once and loads every workload of every
// cluster (one row per workload per cluster: names may differ between
// clusters), filtered by filter, which the user can edit. Marked rows (or the
// one under the cursor) are returned. With a filter that matches exactly one
// workload, no picker.
func (a *app) pickTargets(k string, clusters []kube.Context, filter string, skipped *[]string) ([]target, error) {
	byKey := map[string]target{}
	var all []target
	load := func() ([]tui.Item, error) {
		var sk []string
		all, sk = a.findWorkloads(k, clusters, "")
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
			return nil, err
		}
		var hits []target
		for _, t := range all {
			if strings.Contains(strings.ToLower(t.Name), strings.ToLower(filter)) {
				hits = append(hits, t)
			}
		}
		if len(hits) == 1 {
			return hits, nil
		}
		return runTargetPicker(tui.Options{Title: "Run in which workloads?", Items: items, Query: filter}, byKey)
	}
	return runTargetPicker(tui.Options{Title: fmt.Sprintf("Run in which workloads? (%d clusters)", len(clusters)), Load: load}, byKey)
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

func runningPods(k, kubeconfig, ctxName, ns, selector string) ([]string, error) {
	if selector == "" {
		return nil, errors.New("workload has no matchLabels selector")
	}
	out, err := kubectlOut(k, kubeconfig, 30*time.Second, "--context", ctxName, "-n", ns, "get", "pods", "-l", selector, "-o", "json")
	if err != nil {
		return nil, err
	}
	ps, err := parsePods([]byte(out))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range ps {
		if p.Phase == "Running" && p.Reason != "Terminating" {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func execInPod(k, kubeconfig, ctxName, ns, pod, container string, command []string) (string, error) {
	args := []string{"--context", ctxName, "-n", ns, "exec", pod}
	if container != "" {
		args = append(args, "-c", container)
	}
	args = append(append(args, "--"), command...)
	return kubectlOut(k, kubeconfig, 2*time.Minute, args...)
}

// kubectlOut runs kubectl with the master kubeconfig; the error carries its output.
func kubectlOut(k, kubeconfig string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, k, args...)
	cmd.Env = setEnv(os.Environ(), "KUBECONFIG", kubeconfig)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	if ctx.Err() != nil {
		return buf.String(), fmt.Errorf("timed out after %s", timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(buf.String())
		if msg == "" {
			msg = err.Error()
		}
		return buf.String(), errors.New(msg)
	}
	return buf.String(), nil
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
