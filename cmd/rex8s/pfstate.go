package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vitalbh/rex8s/internal/desktop"
	"github.com/vitalbh/rex8s/internal/forward"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// The background port-forwards (rex8s _pf) are kept in forward.Registry, in
// ~/.cache/rex8s/pf: `kpf -l` and the Raycast menu bar list and stop them.

func (a *app) registry() forward.Registry {
	return forward.Registry{Dir: filepath.Join(a.cache, "pf"), Bin: filepath.Join(a.binDir, "rex8s")}
}

func (a *app) pfList() []forward.Entry { return a.registry().List() }

// kpfList: the running port-forwards; enter opens, copies or stops one.
func (a *app) kpfList() error {
	es := a.pfList()
	if len(es) == 0 {
		fmt.Println(tui.Dim.Render("no port-forwards running · kpf to start one"))
		holdForReading()
		return nil
	}
	byKey := map[string]forward.Entry{}
	items := make([]tui.Item, 0, len(es))
	cw, sw := 0, 0
	for _, e := range es {
		cw = max(cw, len(e.Short))
		sw = max(sw, len(e.Namespace)+1+len(e.Service)+1+len(strconv.Itoa(e.Remote)))
	}
	for _, e := range es {
		k := strconv.Itoa(e.PID)
		byKey[k] = e
		items = append(items, tui.Item{Key: k, Badge: prodBadgeBool(e.Prod),
			Label: fmt.Sprintf("%-*s  %-*s → :%d", cw, e.Short, sw, fmt.Sprintf("%s/%s:%d", e.Namespace, e.Service, e.Remote), e.Local),
			Hint:  strings.TrimSpace(string(e.Kind) + "  since " + kube.Age(e.Started))})
	}
	it, err := tui.Run(tui.Options{Title: fmt.Sprintf("Port-forwards (%d running)", len(es)), Items: items})
	if err != nil || it == nil {
		return err
	}
	e := byKey[it.Key]
	open := "open " + e.URL() + " in the browser"
	if !desktop.CanOpen() {
		open = "print " + e.URL()
	}
	act, err := tui.Run(tui.Options{Title: fmt.Sprintf("%s/%s on %s → %s", e.Namespace, e.Service, e.Short, e.URL()), Items: []tui.Item{
		{Key: "open", Label: fmt.Sprintf("%-6s", "open"), Hint: open},
		{Key: "copy", Label: fmt.Sprintf("%-6s", "copy"), Hint: "copy " + e.URL()},
		{Key: "save", Label: fmt.Sprintf("%-6s", "save"), Hint: "save as a favorite (kpf NAME starts it again, also in Raycast)"},
		{Key: "stop", Label: fmt.Sprintf("%-6s", "stop"), Hint: "stop it (frees the local port)"},
	}})
	if err != nil || act == nil {
		return err
	}
	switch act.Key {
	case "open":
		if err := desktop.OpenURL(e.URL()); err != nil || desktop.CanOpen() {
			return err
		}
		holdForReading() // printed instead
	case "copy":
		if err := desktop.Copy(e.URL()); err != nil {
			return err
		}
		if desktop.CanOpen() {
			fmt.Println("copied " + e.URL())
		} else {
			holdForReading()
		}
	case "save":
		name, _ := tui.AskLine(tui.Bold.Render("Favorite name") + tui.Dim.Render(" (enter: "+e.Service+")") + "\n  > ")
		saved, err := a.saveFavorite(e, name)
		if err != nil {
			return err
		}
		fmt.Printf("saved %s · %s\n", tui.Bold.Render(saved), tui.Dim.Render("kpf "+saved+" starts it and opens the browser"))
		holdForReading()
	case "stop":
		if err := a.registry().Stop(e.PID); err != nil {
			return err
		}
		fmt.Printf("stopped %s/%s on %s\n", e.Namespace, e.Service, e.Short)
	}
	return nil
}

func prodBadgeBool(prod bool) string {
	if prod {
		return "PROD"
	}
	return ""
}
