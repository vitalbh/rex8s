package main

import (
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

	"github.com/vitalbh/rex8s/internal/guard"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// krun runs a command in every pod of a workload, on every cluster that has
// it: `krun nginx -- nginx -s reload`. The workload is found by name
// (Deployments, StatefulSets, DaemonSets, all namespaces); production clusters
// ask for confirmation once for the whole run.
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

	fmt.Fprintln(os.Stderr, tui.Dim.Render(fmt.Sprintf("looking for %q in %d clusters…", filter, len(clusters))))
	found, skipped := a.findWorkloads(k, clusters, filter)
	for _, s := range skipped {
		fmt.Fprintln(os.Stderr, tui.Warn.Render("  "+s))
	}
	if len(found) == 0 {
		return fmt.Errorf("no deployment, statefulset or daemonset matching %q", filter)
	}
	targets, err := pickTargets(found, filter)
	if err != nil || len(targets) == 0 {
		return err
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
	if len(prod) > 0 && os.Getenv("REX8S_FORCE") == "" {
		if !guard.Confirm(strings.Join(prod, "+"), "kubectl exec -- "+cmdText,
			fmt.Sprintf("runs in %d pod(s) of %d workload(s)", len(pods), len(targets))) {
			return nil
		}
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

// parseKrunArgs: krun [-c CONTAINER] FILTER -- CMD...  ("--" optional).
func parseKrunArgs(args []string) (filter, container string, command []string, err error) {
	usage := errors.New("usage: krun [-c container] NAME -- COMMAND...   e.g. krun nginx -- nginx -s reload")
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			command = args[i+1:]
			i = len(args)
		case a == "-c" && i+1 < len(args) && filter == "":
			container = args[i+1]
			i++
		case filter == "":
			filter = a
		default:
			command = args[i:]
			i = len(args)
		}
	}
	if filter == "" || len(command) == 0 {
		return "", "", nil, usage
	}
	return filter, container, command, nil
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
			if strings.Contains(strings.ToLower(w.Name), f) {
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

// pickTargets: the matches, one row per workload per cluster, to mark.
func pickTargets(found []target, filter string) ([]target, error) {
	if len(found) == 1 {
		return found, nil
	}
	byKey := map[string]target{}
	cw, ww := 0, 0
	for _, t := range found {
		cw = max(cw, len(t.c.Short))
		ww = max(ww, len(t.Namespace)+1+len(t.Name))
	}
	items := make([]tui.Item, 0, len(found))
	for _, t := range found {
		byKey[t.key()] = t
		badge := ""
		if t.c.Prod {
			badge = "PROD"
		}
		items = append(items, tui.Item{Key: t.key(), Badge: badge,
			Label: fmt.Sprintf("%-*s  %-6s %-*s", cw, t.c.Short, kube.ShortKind(t.Kind), ww, t.Namespace+"/"+t.Name),
			Hint:  t.c.Name})
	}
	its, err := tui.RunMulti(tui.Options{Title: fmt.Sprintf("Run in which workloads? (%q)", filter), Items: items})
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
