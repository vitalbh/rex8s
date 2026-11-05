package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/pathx"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kwhy: pick a pod and ask Claude why it is unhealthy. It gathers
// `kubectl describe` (env values redacted), the pod's events and recent logs
// (plus the previous container's when it restarted) and sends them to
// `claude -p` with tools disabled, in a pane next to this one.
func (a *app) kwhy(filter string) error {
	if _, err := a.aiCommand(); err != nil {
		return err
	}
	sel, err := a.pickPod("Explain", filter)
	if err != nil || sel == nil {
		return err
	}
	if rex.Block() == "" {
		return a.whytail(sel.ctx.Name, sel.ns, sel.pod.Name)
	}
	return rex.SplitBehind("why "+sel.pod.Name, filepath.Join(a.binDir, "rex8s"), "whytail", sel.ctx.Name, sel.ns, sel.pod.Name)
}

// aiCommand resolves the CLI used by kwhy (config ai_command).
func (a *app) aiCommand() (string, error) {
	name := strings.TrimSpace(a.cfg.AICommand)
	switch strings.ToLower(name) {
	case "off", "none", "false":
		return "", errors.New("kwhy is disabled (ai_command: off in " + a.cfg.File() + ")")
	case "":
		for _, n := range []string{"claude-auto", "claude"} {
			if p := pathx.Real(n, a.binDir); p != "" {
				return p, nil
			}
		}
		return "", errors.New("claude CLI not found in PATH (or set ai_command in " + a.cfg.File() + ")")
	}
	if p := pathx.Real(name, a.binDir); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("ai_command %q not found in PATH", name)
}

const whySystemPrompt = `You are a senior Kubernetes SRE helping an engineer understand why a pod is unhealthy.
You get kubectl describe output, events and logs. Workloads are deployed with Flux (GitOps), so fixes
belong in the Git repository, not in manual kubectl changes.
Reply with:
1. **Diagnosis** — the most likely root cause in one or two sentences, citing the evidence (event, log line, condition).
2. **Evidence** — the few lines that prove it.
3. **Fix** — concrete next steps (what to change in the manifests/values, or which kubectl command to run to confirm).
Be concise. If the data is not enough, say what to check next. If the pod looks healthy, say so.`

// whytail gathers the pod's state and streams Claude's explanation (runs in its own pane).
func (a *app) whytail(ctxName, ns, podName string) error {
	c, err := a.context(ctxName)
	if err != nil {
		return err
	}
	rex.Rename("why " + c.Short + "/" + podName)
	ai, err := a.aiCommand()
	if err != nil {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	cl.Timeout = 20 * time.Second

	fmt.Println(tui.Bold.Render("kwhy") + tui.Dim.Render("  "+c.Short+"/"+ns+"/"+podName))
	fmt.Print(tui.Dim.Render("gathering describe, events and logs… "))
	report := gatherPod(cl, ctxName, ns, podName)
	fmt.Println(tui.Dim.Render(fmt.Sprintf("%d KB", len(report)/1024)))

	system := whySystemPrompt
	if lang := strings.TrimSpace(a.cfg.AILanguage); lang != "" {
		system += "\nAnswer in " + lang + "."
	}
	fmt.Println(tui.Dim.Render("asking " + filepath.Base(ai) + "… (tools disabled; the data above is all it sees)\n"))
	cmd := exec.Command(ai, "-p", "--tools", "", "--system-prompt", system)
	cmd.Stdin = strings.NewReader(report)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	runErr := cmd.Run()
	if runErr != nil {
		fmt.Println(tui.Err.Render("\n" + filepath.Base(ai) + ": " + runErr.Error()))
	}
	fmt.Fprint(os.Stderr, tui.Dim.Render("\npress enter to close"))
	bufio.NewReader(os.Stdin).ReadString('\n')
	return nil
}

// gatherPod builds the report sent to the model, capped to keep it small.
func gatherPod(cl kube.Client, ctxName, ns, podName string) string {
	var b strings.Builder
	section := func(title, body string, max int) {
		body = strings.TrimSpace(body)
		if len(body) > max {
			body = "…(truncated)…\n" + body[len(body)-max:]
		}
		fmt.Fprintf(&b, "## %s\n```\n%s\n```\n\n", title, body)
	}
	fmt.Fprintf(&b, "Pod %s in namespace %s (cluster context %s).\n\n", podName, ns, ctxName)

	if out, err := cl.Get(ctxName, "describe", "pod", podName, "-n", ns); err == nil {
		section("kubectl describe pod", redactEnv(string(out)), 12000)
	} else {
		section("kubectl describe pod (failed)", err.Error(), 2000)
	}
	if out, err := cl.Get(ctxName, "get", "events", "-n", ns, "-o", "json",
		"--field-selector", "involvedObject.name="+podName); err == nil {
		if evs, err := parseEvents(out); err == nil {
			var lines []string
			for _, e := range evs {
				lines = append(lines, fmt.Sprintf("%s %s %s (x%d): %s", e.When.Format(time.RFC3339), e.Type, e.Reason, max(e.Count, 1), oneLine(e.Message)))
			}
			section("events", strings.Join(lines, "\n"), 6000)
		}
	}
	logs := func(prev bool) (string, error) {
		args := []string{"logs", podName, "-n", ns, "--all-containers", "--prefix", "--tail=120"}
		if prev {
			args = append(args, "--previous")
		}
		out, err := cl.Get(ctxName, args...)
		return string(bytes.ToValidUTF8(out, []byte("?"))), err
	}
	if out, err := logs(false); err == nil {
		section("logs (current, last 120 lines per container)", out, 14000)
	} else {
		section("logs (current) unavailable", err.Error(), 2000)
	}
	if out, err := logs(true); err == nil && strings.TrimSpace(out) != "" {
		section("logs (previous container, before the last restart)", out, 14000)
	}
	return b.String()
}

var envLine = regexp.MustCompile(`^(\s+[A-Za-z_][A-Za-z0-9_.-]*:)\s+(.+)$`)

// redactEnv hides literal values in the "Environment:" blocks of kubectl
// describe (references like "<set to the key ...>" are kept: they are names,
// not secrets).
func redactEnv(describe string) string {
	lines := strings.Split(describe, "\n")
	inEnv, envIndent := false, 0
	for i, l := range lines {
		trimmed := strings.TrimLeft(l, " ")
		indent := len(l) - len(trimmed)
		if strings.HasPrefix(trimmed, "Environment:") {
			inEnv, envIndent = true, indent
			continue
		}
		if inEnv {
			if trimmed == "" || indent <= envIndent {
				inEnv = false
				continue
			}
			if m := envLine.FindStringSubmatch(l); m != nil && !strings.HasPrefix(m[2], "<set to") && !strings.HasPrefix(m[2], "(v1:") {
				lines[i] = m[1] + " <redacted>"
			}
		}
	}
	return strings.Join(lines, "\n")
}
