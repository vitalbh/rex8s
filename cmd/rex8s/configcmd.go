package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/tui"
)

// configKeys: the settings rex8s config can change, with what each takes.
var configKeys = map[string]string{
	"kubeconfig":       "path of the master kubeconfig",
	"prod_background":  "hex color for production panes, or off (ktint picks one)",
	"monitor_interval": "how often the Flux monitor polls, e.g. 2m",
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
			return a.editConfig(func(doc string) string { return setYAMLKey(doc, key, val) })
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
		return a.editConfig(func(doc string) string { return setYAMLKey(doc, key, yamlScalar(val)) })
	case "unset":
		if err := need(2); err != nil {
			return err
		}
		return a.editConfig(func(doc string) string { return removeYAMLKey(doc, args[1]) })
	case "add":
		if err := need(3); err != nil {
			return err
		}
		if !listKeys[args[1]] {
			return fmt.Errorf("%s is not a list: rex8s config set %s VALUE", args[1], args[1])
		}
		val := strings.Join(args[2:], " ")
		return a.editConfig(func(doc string) string { return addYAMLFlowItem(doc, args[1], yamlScalar(val)) })
	case "remove":
		if err := need(3); err != nil {
			return err
		}
		if !listKeys[args[1]] {
			return fmt.Errorf("%s is not a list", args[1])
		}
		val := strings.Join(args[2:], " ")
		return a.editConfig(func(doc string) string { return removeYAMLListItem(doc, args[1], val) })
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

// editConfig is the one way rex8s writes config.yaml: read, change the text
// (comments and order are kept), refuse if the file changed meanwhile (the
// user saved it), write atomically, and put it back if it no longer parses.
func (a *app) editConfig(change func(doc string) string) error {
	path := a.configPath()
	before, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	st, _ := os.Stat(path)
	after := change(string(before))
	if after == string(before) {
		fmt.Println(tui.Dim.Render("nothing to change"))
		return nil
	}
	if st2, _ := os.Stat(path); (st == nil) != (st2 == nil) || (st != nil && !st.ModTime().Equal(st2.ModTime())) {
		return fmt.Errorf("%s changed while rex8s was editing it: run the command again", tildePath(path))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(after), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		_ = os.WriteFile(path, before, 0o644)
		return fmt.Errorf("the change broke the file, so it was undone: %v", err)
	}
	a.cfg = cfg
	fmt.Println(tui.Ok.Render("✓ ") + "saved " + tildePath(path))
	return nil
}

func removeYAMLKey(doc, key string) string {
	lines := strings.Split(doc, "\n")
	var out []string
	skipping := false
	for _, l := range lines {
		if strings.HasPrefix(l, key+":") {
			skipping = true
			continue
		}
		if skipping && (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "-")) {
			continue // the key's block list or map
		}
		skipping = false
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// addYAMLFlowItem adds VALUE to a list written either way: "key: [a, b]" or
// a block list.
func addYAMLFlowItem(doc, key, value string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*\[(.*?)\](.*)$`)
	if m := re.FindStringSubmatchIndex(doc); m != nil {
		inner := strings.TrimSpace(doc[m[2]:m[3]])
		for _, it := range strings.Split(inner, ",") {
			if strings.Trim(strings.TrimSpace(it), `"'`) == strings.Trim(value, `"'`) {
				return doc
			}
		}
		if inner != "" {
			inner += ", "
		}
		return doc[:m[0]] + key + ": [ " + strings.TrimSpace(inner+value) + " ]" + doc[m[4]:m[5]] + doc[m[1]:]
	}
	if strings.Contains(doc, "\n  - "+value+"\n") {
		return doc
	}
	return addYAMLListItem(doc, key, value)
}

func removeYAMLListItem(doc, key, value string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*\[(.*?)\](.*)$`)
	if m := re.FindStringSubmatchIndex(doc); m != nil {
		var keep []string
		for _, it := range strings.Split(doc[m[2]:m[3]], ",") {
			t := strings.TrimSpace(it)
			if t != "" && strings.Trim(t, `"'`) != value {
				keep = append(keep, t)
			}
		}
		return doc[:m[0]] + key + ": [ " + strings.Join(keep, ", ") + " ]" + doc[m[4]:m[5]] + doc[m[1]:]
	}
	var out []string
	in := false
	for _, l := range strings.Split(doc, "\n") {
		if strings.HasPrefix(l, key+":") {
			in = true
		} else if in && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "-") && strings.TrimSpace(l) != "" {
			in = false
		}
		if in {
			t := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "-"))
			if c := yamlComment(t); c != "" {
				t = strings.TrimSpace(strings.TrimSuffix(t, c))
			}
			if strings.Trim(t, `"'`) == value && strings.HasPrefix(strings.TrimSpace(l), "-") {
				continue
			}
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}
