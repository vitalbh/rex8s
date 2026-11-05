package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// tintPresets: dark backgrounds that keep normal text readable.
var tintPresets = []struct{ name, color string }{
	{"red (default)", "#2b1216"},
	{"wine", "#2a1020"},
	{"purple", "#1f1430"},
	{"navy", "#101a2e"},
	{"teal", "#0f2224"},
	{"forest", "#13210f"},
	{"amber", "#2a1e0c"},
	{"graphite", "#232323"},
	{"off", "off"},
}

var hexColor = regexp.MustCompile(`^#?[0-9A-Fa-f]{6}$`)

// ktint changes the background of production panes:
//
//	ktint              picker with presets, previewed live; enter saves
//	ktint #1f1430      this pane only, now (try a color)
//	ktint #1f1430 --save   …and save it for every production pane
//	ktint off [--save] no tint
//	ktint reset        back to the configured color
func (a *app) ktint(args []string) error {
	save := false
	var color string
	for _, arg := range args {
		switch {
		case arg == "--save" || arg == "-s":
			save = true
		case color == "":
			color = arg
		}
	}
	switch color {
	case "":
		return a.ktintPick()
	case "reset":
		a.reapplyTint()
		fmt.Println("tint: " + describeTint(a.cfg.Tint()))
		return nil
	}
	norm, err := normTint(color)
	if err != nil {
		return err
	}
	applyTint(norm)
	if !save {
		fmt.Println(tui.Dim.Render("this pane only · ktint " + color + " --save to keep it · ktint reset to undo"))
		return nil
	}
	return a.saveTint(norm)
}

func normTint(c string) (string, error) {
	switch strings.ToLower(c) {
	case "off", "none", "false":
		return "off", nil
	}
	if !hexColor.MatchString(c) {
		return "", fmt.Errorf("%q: use a hex color like #1f1430, or off", c)
	}
	return "#" + strings.ToLower(strings.TrimPrefix(c, "#")), nil
}

func applyTint(c string) {
	if c == "off" {
		rex.SetTheme("")
	} else {
		rex.SetTheme(c)
	}
}

func describeTint(c string) string {
	if c == "" {
		return "off"
	}
	return c
}

// reapplyTint puts this pane back to what the config says for its cluster.
func (a *app) reapplyTint() {
	if c, err := kube.Current(a.cfg, "", ""); err == nil {
		a.tint(c)
	} else {
		rex.SetTheme("")
	}
}

func (a *app) ktintPick() error {
	current := a.cfg.Tint()
	items := make([]tui.Item, 0, len(tintPresets))
	selected := ""
	for _, p := range tintPresets {
		items = append(items, tui.Item{Key: p.color, Label: fmt.Sprintf("%-14s", p.name), Hint: p.color})
		if p.color == current || (current == "" && p.color == "off") {
			selected = p.color
		}
	}
	if selected == "" && current != "" {
		items = append([]tui.Item{{Key: current, Label: fmt.Sprintf("%-14s", "current"), Hint: current}}, items...)
		selected = current
	}
	last := ""
	it, err := tui.Run(tui.Options{
		Title:    "Production background · moving previews it here, enter saves",
		Items:    items,
		Selected: selected,
		PreviewHead: func(it tui.Item) string {
			if it.Key != last { // live preview on this pane
				last = it.Key
				applyTint(it.Key)
			}
			if it.Key == "off" {
				return tui.Bold.Render("No tint") + "\n\n" + tui.Dim.Render("Production panes look like any other; the PROD label and the confirmation stay.")
			}
			swatch := lipgloss.NewStyle().Background(lipgloss.Color(it.Key)).Foreground(lipgloss.Color("252")).Padding(1, 2).
				Render("kubectl get pods -n api\nNAME                  READY   STATUS\napi-7d9f6c5b8-4kq2x   1/1     Running")
			return tui.Bold.Render(strings.TrimSpace(it.Label)+"  "+it.Key) + "\n\n" + swatch
		},
	})
	if err != nil || it == nil {
		a.reapplyTint()
		return err
	}
	if err := a.saveTint(it.Key); err != nil {
		a.reapplyTint()
		return err
	}
	return nil
}

// saveTint writes prod_background to the config file (keeping the rest of
// it as is) and re-applies it here.
func (a *app) saveTint(color string) error {
	if os.Getenv("REX8S_DEMO") != "" && !strings.HasPrefix(a.configPath(), os.Getenv("REX8S_DEMO")) {
		return errors.New("demo mode: not touching the real config")
	}
	if err := a.editConfig(func(doc string) string { return setYAMLKey(doc, "prod_background", fmt.Sprintf("%q", color)) }); err != nil {
		return err
	}
	a.reapplyTint()
	fmt.Println(tui.Dim.Render("new production panes use " + color + "; in open ones run rex8s label (or kctx)"))
	return nil
}

// setYAMLKey replaces a top-level "key: …" line (keeping its comment), or
// appends one.
func setYAMLKey(doc, key, value string) string {
	lines := strings.Split(doc, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, key+":") {
			continue
		}
		line := key + ": " + value
		if c := yamlComment(l); c != "" {
			line = fmt.Sprintf("%-42s %s", line, c)
		}
		lines[i] = line
		return strings.Join(lines, "\n")
	}
	if doc != "" && !strings.HasSuffix(doc, "\n") {
		doc += "\n"
	}
	return doc + key + ": " + value + "\n"
}

// yamlComment: the "# …" of a line, outside quotes ("" when none).
func yamlComment(l string) string {
	var quote rune
	for i, r := range l {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#' && i > 0 && (l[i-1] == ' ' || l[i-1] == '\t'):
			return l[i:]
		}
	}
	return ""
}
