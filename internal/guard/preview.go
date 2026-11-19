package guard

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// What an agent's production change would do, shown next to the Rex
// palette's question: kubectl apply/create/replace → kubectl diff; kubectl
// delete → the objects (kubectl get). Best effort: when kubectl can't tell
// (a flag diff/get doesn't take, the server unreachable) there is no preview,
// and the question shows the command alone.

const previewTimeout = 20 * time.Second

// previewArgs: the kubectl arguments that show what args would do, or nil;
// isDiff when they run kubectl diff. The verb is swapped in place, so the
// context, namespace and files stay.
func previewArgs(tool string, p Parsed, args []string) (pargs []string, isDiff bool) {
	if tool != "kubectl" || p.DryRun || p.Help {
		return nil, false
	}
	var with string
	switch p.Verb {
	case "apply", "replace":
		with = "diff"
	case "create":
		if !hasFileFlag(args) {
			return nil, false // kubectl create deployment …: nothing to diff
		}
		with = "diff"
	case "delete":
		if hasFileFlag(args) || len(p.Rest) > 0 {
			with = "get"
		}
	}
	if with == "" {
		return nil, false
	}
	out := append([]string{}, args...)
	for i, a := range out {
		if a == p.Verb {
			out[i] = with
			if with == "get" {
				out = append(out[:i+1], dropDeleteFlags(out[i+1:])...)
			}
			return out, with == "diff"
		}
	}
	return nil, false
}

func hasFileFlag(args []string) bool {
	for _, a := range args {
		if a == "-f" || a == "-k" || strings.HasPrefix(a, "--filename") || strings.HasPrefix(a, "--kustomize") ||
			strings.HasPrefix(a, "-f=") || strings.HasPrefix(a, "-k=") {
			return true
		}
	}
	return false
}

// dropDeleteFlags: delete's own flags, which get refuses.
func dropDeleteFlags(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name := strings.SplitN(a, "=", 2)[0]
		switch name {
		case "--grace-period", "--timeout", "--cascade", "--wait", "--force", "--now", "--ignore-not-found",
			"--interactive", "-i", "--dry-run", "--raw":
			if !strings.Contains(a, "=") && i+1 < len(args) && (name == "--grace-period" || name == "--timeout") {
				i++ // its value
			}
			continue
		}
		out = append(out, a)
	}
	return out
}

// runPreview runs kubectl (bin) with pargs and returns what it printed, ""
// when it failed. kubectl diff (isDiff) exits 1 when there are differences.
func runPreview(bin string, pargs []string, isDiff bool) string {
	if pargs == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), previewTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, pargs...)
	cmd.Env = append(os.Environ(), "KUBECTL_EXTERNAL_DIFF=diff -u -N")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !isDiff || !errors.As(err, &ee) || ee.ExitCode() != 1 {
			return ""
		}
	}
	s := strings.TrimRight(out.String(), "\n")
	if s == "" && isDiff {
		return "(kubectl diff: no changes)"
	}
	return s
}

// diffStat: "+12 −3" for a unified diff, "" for anything else.
func diffStat(s string) string {
	add, del := 0, 0
	for _, l := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
		case strings.HasPrefix(l, "+"):
			add++
		case strings.HasPrefix(l, "-"):
			del++
		}
	}
	if add == 0 && del == 0 {
		return ""
	}
	return "+" + strconv.Itoa(add) + " −" + strconv.Itoa(del)
}

// colorDiff paints a unified diff for the preview pane.
func colorDiff(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "diff "):
			b.WriteString("\x1b[1m" + l + "\x1b[0m")
		case strings.HasPrefix(l, "+"):
			b.WriteString("\x1b[32m" + l + "\x1b[0m")
		case strings.HasPrefix(l, "-"):
			b.WriteString("\x1b[31m" + l + "\x1b[0m")
		case strings.HasPrefix(l, "@@"):
			b.WriteString("\x1b[36m" + l + "\x1b[0m")
		default:
			b.WriteString(l)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
