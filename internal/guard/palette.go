package guard

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/vitalbh/rex8s/internal/rex"
)

// Asking in the Rex palette: a production change with nobody at a terminal
// (an agent, a script) shows "Claude Code wants to change PRODUCTION use" in
// every connected Rex app — the Mac's palette, the iPhone — with Allow once
// and Deny. Only a single change is allowed this way: unlocking for a while
// stays at the terminal (kunlock), so a person there decides that.
//
// Any Rex app connected to your server can answer, and a program could in
// principle connect as one: like the rest of the guard, this keeps honest
// mistakes out of production, it is not a security boundary.

// paletteTimeout: how long the palette waits. Below the 2 minutes Claude
// Code gives a command by default, so the agent sees the refusal.
const paletteTimeout = 90

// errNoPalette: no Rex app could ask; the policy refuses as before.
var errNoPalette = errors.New("no palette")

// who: what to call the caller in the question.
func who() string {
	switch {
	case sys.getenv("CLAUDECODE") == "1":
		return "Claude Code"
	case sys.getenv("REX8S_AGENT") != "":
		return "An agent"
	}
	return "A command without a terminal"
}

// askPalette asks in the Rex apps. allowed: the person chose Allow once;
// reason: why not (deny, cancelled, timeout); errNoPalette when nobody
// could be asked.
func askPalette(prod []string, what string, maybe bool, preview func() string) (allowed bool, reason string, err error) {
	verb := "change"
	if maybe {
		verb = "maybe change"
	}
	cluster := strings.Join(prod, ", ")
	fmt.Fprintf(sys.stderr, "rex8s guard: PRODUCTION %s — asking in the Rex palette (%ds)…\n", cluster, paletteTimeout)
	detail, closePane := "", func() {}
	if preview != nil {
		if p := preview(); p != "" {
			detail = diffStat(p)
			closePane = showPreview(cluster, p)
		}
	}
	defer closePane()
	a, err := rex.Ask(rex.Prompt{
		Title:   fmt.Sprintf("%s wants to %s PRODUCTION %s", who(), verb, cluster),
		Confirm: "Answer",
		Timeout: paletteTimeout,
		Items: []rex.Item{
			{ID: "allow", Title: "Allow once", Subtitle: what, Detail: detail, Icon: "terminal"},
			{ID: "deny", Title: "Deny", Subtitle: "the command is refused", Icon: "gear"},
		},
	})
	switch {
	case err != nil:
		return false, "", errNoPalette
	case !a.Answered():
		return false, a.Reason, nil
	case a.Value == "allow":
		return true, "", nil
	}
	return false, "deny", nil
}

// showPreview opens a pane next to the caller's (an agent's terminal in
// Rex) with what the change would do, while the palette asks; the returned
// func closes it. Outside Rex nothing opens.
func showPreview(cluster, preview string) (closePane func()) {
	if rex.Block() == "" {
		return func() {}
	}
	f, err := os.CreateTemp("", "rex8s-preview-*.txt")
	if err != nil {
		return func() {}
	}
	_, _ = f.WriteString(colorDiff(preview))
	_ = f.Close()
	script := `cat "$0"; printf '\n\033[2m(the Rex palette is asking)\033[0m\n'; exec sleep 600`
	id, err := rex.SplitBlock(rex.Block(), "right", 45, false, "PROD "+cluster+" preview", "/bin/sh", "-c", script, f.Name())
	if err != nil {
		_ = os.Remove(f.Name())
		return func() {}
	}
	return func() {
		rex.CloseBlock(id)
		_ = os.Remove(f.Name())
	}
}
