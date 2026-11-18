package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/tui"
	"github.com/vitalbh/rex8s/internal/yamledit"
)

// configKeys: the settings rex8s config can change, with what each takes.
var configKeys = map[string]string{
	"kubeconfig":       "path of the master kubeconfig (or a list a:b, like KUBECONFIG)",
	"prod_background":  "hex color for production panes, or off (ktint picks one)",
	"monitor_interval": "how often the Flux monitor polls, e.g. 2m",
	"cluster_browser":  "left pane of the ⇧⌘K layout: kview (default) or k9s",
	"forward_expire":   "a background port-forward unused this long goes away: 8h (default), or off",
	"log_links":        "cluster URLs in logs that become clickable: namespace (default: the stream's), all, or off",
	"login_url":        "page kauth opens when a login expired",
	"notifications":    "true or false: macOS notifications",
	"ai_command":       "CLI for kwhy (claude by default), or off",
	"ai_language":      "language kwhy and kask answer in, e.g. pt-BR",
	"ask_agent":        "agent kask opens: claude (default) or codex",
	"node_debug_image": "image knode runs on the node",
	"net_debug_image":  "image knet runs in the namespace",
	"gitops_repos":     "list: local clones of the Flux repos (config add/remove)",
	"hide":             "list: contexts hidden from the picker (config add/remove)",
}

var listKeys = map[string]bool{"gitops_repos": true, "hide": true}

// rex8s config [get KEY | set KEY VALUE | unset KEY | add KEY VALUE | remove KEY VALUE]
// No arguments: every setting with its current value.
func (a *app) configCmd(args []string) error {
	if len(args) == 0 {
		return a.configList()
	}
	need := func(n int) error {
		if len(args) < n {
			return fmt.Errorf("usage: rex8s config %s KEY%s", args[0], map[bool]string{true: " VALUE", false: ""}[n > 2])
		}
		if _, ok := configKeys[args[1]]; !ok {
			return fmt.Errorf("unknown setting %q (rex8s config lists them; clusters and forwards are edited in %s)", args[1], tildePath(a.configPath()))
		}
		return nil
	}
	switch args[0] {
	case "get":
		if err := need(2); err != nil {
			return err
		}
		fmt.Println(a.configValue(args[1]))
		return nil
	case "set":
		if err := need(3); err != nil {
			return err
		}
		key, val := args[1], strings.Join(args[2:], " ")
		if listKeys[key] {
			return fmt.Errorf("%s is a list: rex8s config add %s VALUE", key, key)
		}
		if key == "notifications" {
			if _, err := strconv.ParseBool(val); err != nil {
				return fmt.Errorf("notifications: true or false")
			}
			return a.editConfig(func(doc string) string { return yamledit.SetKey(doc, key, val) })
		}
		if key == "forward_expire" {
			if err := config.ValidForwardExpire(val); err != nil {
				return err
			}
		}
		if key == "log_links" && val != "namespace" && val != "all" && val != "off" {
			return fmt.Errorf("log_links: namespace, all or off")
		}
		if key == "cluster_browser" && val != "kview" && val != "k9s" {
			return fmt.Errorf("cluster_browser: kview or k9s")
		}
		if key == "ask_agent" && val != "claude" && val != "codex" {
			return fmt.Errorf("ask_agent: claude or codex")
		}
		if key == "prod_background" {
			n, err := normTint(val)
			if err != nil {
				return err
			}
			val = n
		}
		return a.editConfig(func(doc string) string { return yamledit.SetKey(doc, key, yamledit.String(val)) })
	case "unset":
		if err := need(2); err != nil {
			return err
		}
		return a.editConfig(func(doc string) string { return yamledit.RemoveKey(doc, args[1]) })
	case "add":
		if err := need(3); err != nil {
			return err
		}
		if !listKeys[args[1]] {
			return fmt.Errorf("%s is not a list: rex8s config set %s VALUE", args[1], args[1])
		}
		val := strings.Join(args[2:], " ")
		return a.editConfig(func(doc string) string { return yamledit.AddFlowItem(doc, args[1], yamledit.String(val)) })
	case "remove":
		if err := need(3); err != nil {
			return err
		}
		if !listKeys[args[1]] {
			return fmt.Errorf("%s is not a list", args[1])
		}
		val := strings.Join(args[2:], " ")
		return a.editConfig(func(doc string) string { return yamledit.RemoveListItem(doc, args[1], val) })
	}
	return fmt.Errorf("usage: rex8s config [get KEY | set KEY VALUE | unset KEY | add KEY VALUE | remove KEY VALUE]")
}

func (a *app) configList() error {
	keys := make([]string, 0, len(configKeys))
	for k := range configKeys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println(tui.Dim.Render(tildePath(a.configPath())))
	for _, k := range keys {
		v := a.configValue(k)
		if v == "" {
			v = tui.Dim.Render("—")
		}
		fmt.Printf("  %-17s %s\n  %-17s %s\n", k, v, "", tui.Dim.Render(configKeys[k]))
	}
	fmt.Println(tui.Dim.Render("\nrex8s config set KEY VALUE · unset KEY · add/remove KEY VALUE (lists) · clusters and forwards: edit the file"))
	return nil
}

// configValue: the value as written in the file ("" when unset).
func (a *app) configValue(key string) string {
	data, err := os.ReadFile(a.configPath())
	if err != nil {
		return ""
	}
	var m map[string]any
	if yaml.Unmarshal(data, &m) != nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if l, ok := v.([]any); ok {
		parts := make([]string, len(l))
		for i, x := range l {
			parts[i] = fmt.Sprint(x)
		}
		return strings.Join(parts, ", ")
	}
	return fmt.Sprint(v)
}

// editConfig is the one way rex8s writes config.yaml (yamledit.Edit): the
// text changes in place (comments and order are kept), a file the user
// saved meanwhile is left alone, and a change that breaks it is undone.
// On success a.cfg is the new config.
func (a *app) editConfig(change func(doc string) string) error {
	path := a.configPath()
	var cfg *config.Config
	res, err := yamledit.Edit(path, change, func() error {
		c, err := config.Load()
		cfg = c
		return err
	})
	if errors.Is(err, yamledit.ErrChanged) {
		return fmt.Errorf("%s changed while rex8s was editing it: run the command again", tildePath(path))
	}
	if err != nil {
		return err
	}
	if !res.Changed {
		fmt.Println(tui.Dim.Render("nothing to change"))
		return nil
	}
	a.cfg = cfg
	fmt.Println(tui.Ok.Render("✓ ") + "saved " + tildePath(path))
	for _, w := range cfg.Warnings {
		fmt.Println(tui.Warn.Render("! ") + w)
	}
	return nil
}
