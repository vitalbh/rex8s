package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kmulti opens a tab with one pane per cluster — each an isolated rex8s
// pane (own context, label, production tint) — side by side, or in a grid
// from four on.
//
//	kmulti                       pick the clusters (space marks)
//	kmulti prod                  a group from the config (groups: {prod: [dal, sp]})
//	kmulti -n api get pods       every pane runs the command first, then stays a shell
//	kmulti -A                    every namespace in each pane
//
// In that tab, kall CMD types CMD in every cluster pane at once.
func (a *app) kmulti(args []string) error {
	if rex.Block() == "" {
		return errors.New("kmulti opens a Rex tab: run it inside Rex")
	}
	ns, group, closeSelf := "", "", false
	var cmd []string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--close" && len(cmd) == 0:
			closeSelf = true
		case (arg == "-n" || arg == "--namespace") && i+1 < len(args) && len(cmd) == 0:
			ns = args[i+1]
			i++
		case (arg == "-A" || arg == "--all-namespaces") && len(cmd) == 0:
			ns = kube.AllNamespaces
		case arg == "--" && len(cmd) == 0:
			cmd = append(cmd, args[i+1:]...)
			i = len(args)
		case group == "" && len(cmd) == 0 && a.cfg.Groups[arg] != nil:
			group = arg
		default:
			cmd = append(cmd, arg)
		}
	}
	// a bare "get pods" means kubectl get pods
	if len(cmd) > 0 && kubectlVerbs[cmd[0]] {
		cmd = append([]string{"kubectl"}, cmd...)
	}

	raw, err := kube.Load(a.src)
	if err != nil {
		return err
	}
	all := uniqueClusters(kube.Contexts(raw, a.cfg))
	var chosen []kube.Context
	if group != "" {
		for _, short := range a.cfg.Groups[group] {
			c, err := a.clusterContext(short)
			if err != nil {
				return fmt.Errorf("group %s: %v", group, err)
			}
			chosen = append(chosen, c)
		}
	} else {
		byKey := map[string]kube.Context{}
		items := make([]tui.Item, 0, len(all))
		for _, c := range all {
			byKey[c.Name] = c
			items = append(items, tui.Item{Key: c.Name, Label: fmt.Sprintf("%-8s", c.Short), Hint: c.Name, Badge: prodBadgeBool(c.Prod)})
		}
		title := "Which clusters, side by side? (space marks, ctrl+a all)"
		if len(cmd) > 0 {
			title = "Run " + strings.Join(cmd, " ") + " on which clusters?"
		}
		its, err := tui.RunMulti(tui.Options{Title: title, Items: items})
		if err != nil || len(its) == 0 {
			if closeSelf {
				rex.CloseSelf()
			}
			return err
		}
		for _, it := range its {
			chosen = append(chosen, byKey[it.Key])
		}
	}
	if len(chosen) == 0 {
		return nil
	}

	var ctxs []string
	for _, c := range chosen {
		ctxs = append(ctxs, c.Name)
	}
	argsJSON, _ := json.Marshal(map[string]string{"ctxs": strings.Join(ctxs, ","), "ns": ns, "cmd": strings.Join(cmd, " ")})
	out, err := exec.Command(rex.Bin(), "-s", rex.Session(), "do", "k8s_multi", "--args", string(argsJSON), "--json").CombinedOutput()
	if err != nil {
		return fmt.Errorf("k8s_multi: %s (rex8s setup updates rex8s.lua)", strings.TrimSpace(string(out)))
	}
	var res struct {
		WindowID string `json:"window_id"`
	}
	_ = json.Unmarshal(out, &res)
	showWindow(res.WindowID)
	var shorts []string
	for _, c := range chosen {
		shorts = append(shorts, c.Short)
	}
	fmt.Printf("opened %s · %s\n", strings.Join(shorts, " | "), tui.Dim.Render("kall CMD types CMD in all of them"))
	if closeSelf {
		rex.CloseSelf()
	}
	return nil
}

var kubectlVerbs = set("get", "describe", "top", "logs", "events", "rollout", "explain", "api-resources", "auth", "exec", "port-forward", "diff")

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// showWindow asks the app to show a tab this process opened (needs Remote
// Control on; harmless when off).
func showWindow(windowID string) {
	if windowID == "" {
		return
	}
	out, err := exec.Command(rex.Bin(), "do", "-e", `return rex.call("client.list")`, "--json").Output()
	if err != nil {
		return
	}
	var cl struct {
		Clients []struct {
			ID   string `json:"client_id"`
			Info *struct {
				Kind string `json:"kind"`
			} `json:"info"`
		} `json:"clients"`
	}
	if json.Unmarshal(out, &cl) != nil {
		return
	}
	for _, c := range cl.Clients {
		if c.Info != nil && c.Info.Kind == "app" {
			_ = exec.Command(rex.Bin(), "-C", c.ID, "do", "session.select", "session_id="+rex.Session(), "window_id="+windowID).Run()
			return
		}
	}
}

// kall types a command in every rex8s cluster pane of this tab (this one
// included), as if you typed it in each: each runs it on its own cluster,
// and production panes still ask before a change.
func (a *app) kall(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: kall CMD... (e.g. kall kubectl get pods -n api)")
	}
	if rex.Block() == "" {
		return errors.New("kall works in a Rex tab with cluster panes (kmulti)")
	}
	cmd := args
	if kubectlVerbs[cmd[0]] {
		cmd = append([]string{"kubectl"}, cmd...)
	}
	text := strings.Join(cmd, " ")

	var me struct {
		Block struct {
			WindowID string `json:"window_id"`
		} `json:"block"`
	}
	out, err := exec.Command(rex.Bin(), "block", "inspect", rex.Block(), "--json").Output()
	if err != nil || json.Unmarshal(out, &me) != nil || me.Block.WindowID == "" {
		return errors.New("could not tell which tab this pane is in")
	}
	out, err = exec.Command(rex.Bin(), "api", "call", "-s", rex.Session(), "session.list_blocks", "{}").Output()
	if err != nil {
		return fmt.Errorf("rex: %v", err)
	}
	var list struct {
		Blocks []struct {
			ID       string `json:"block_id"`
			Label    string `json:"label"`
			WindowID string `json:"window_id"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return err
	}
	var targets []string
	var labels []string
	for _, b := range list.Blocks {
		if b.WindowID != me.Block.WindowID || !isClusterPaneLabel(b.Label) {
			continue
		}
		targets = append(targets, b.ID)
		labels = append(labels, b.Label)
	}
	if len(targets) == 0 {
		return errors.New("no rex8s cluster panes in this tab")
	}
	sort.Strings(labels)
	for _, id := range targets {
		if out, err := exec.Command(rex.Bin(), "send", "-b", id, text+"\r").CombinedOutput(); err != nil {
			return fmt.Errorf("rex send: %s", strings.TrimSpace(string(out)))
		}
	}
	fmt.Fprintln(os.Stderr, tui.Dim.Render(fmt.Sprintf("sent to %d pane(s): %s", len(targets), strings.Join(labels, " · "))))
	return nil
}

// rex8s cluster panes are labeled "PROD dal/ns" or "k8s staging/ns".
func isClusterPaneLabel(l string) bool {
	return strings.HasPrefix(l, "PROD ") || strings.HasPrefix(l, "k8s ")
}
