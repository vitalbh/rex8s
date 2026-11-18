package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/vitalbh/rex8s/internal/tui"
)

var helpKeys = []struct{ keys, what string }{
	{"⇧⌘J", "new tab: pick a cluster → isolated shell"},
	{"⇧⌘K", "new tab: pick a cluster → kview · shell · warnings"},
	{"⇧⌘V", "new tab: kview on every cluster"},
	{"⇧⌘M", "new tab: pick clusters → one pane each, side by side"},
	{"⇧⌘I", "cluster shell ⇄ kview of the same cluster (opens one in a split)"},
	{"⌘⇧P", "palette: Kubernetes: Flux Monitor, Restore Cluster Tabs, kpf favorites, kall…"},
}

// khelp: every rex8s command in a picker; enter runs the one under the cursor.
func (a *app) khelp(args []string) error {
	filter := strings.Join(args, " ")
	byKey := map[string]helpEntry{}
	cw := 0
	for _, e := range helpRows {
		cw = max(cw, len(e.cmd))
	}
	var items []tui.Item
	for _, e := range helpRows {
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
	// a k* command is rex8s run under that name (it dispatches on argv[0]),
	// so it works without the k* links too (wrappers: false)
	bin := filepath.Join(a.binDir, argv[0])
	if _, ok := wrapperOf[argv[0]]; ok {
		bin = filepath.Join(a.binDir, "rex8s")
	}
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("%s not installed (make install)", argv[0])
	}
	fmt.Println(tui.Dim.Render("$ " + e.cmd))
	return syscall.Exec(bin, argv, os.Environ())
}
