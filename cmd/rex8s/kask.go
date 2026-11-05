package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/pathx"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kask opens a Claude Code conversation in a split, already holding context
// about this pane's cluster:
//
//	kask [FILTER]           picker: the whole cluster (a snapshot of its
//	                        problems) or one pod (describe, events, logs)
//	kubectl logs x | kask   whatever comes through the pipe
//	kask --dump [FILTER]    only save the context file and print its path
//
// The context is saved as a Markdown file in ~/.cache/rex8s/dumps (Claude
// runs there and reads it). Read-only kubectl/flux commands run without
// asking; anything else asks for approval in Claude, and changes to a
// production cluster are refused by the guard unless re-run with
// REX8S_FORCE=1 (a new command, so a new approval).
func (a *app) kask(args []string) error {
	if _, err := a.claudeBin(); err != nil {
		return err
	}
	piped := !term.IsTerminal(int(os.Stdin.Fd()))
	dumpOnly := false
	var rest []string
	for _, a := range args {
		if a == "--dump" {
			dumpOnly = true
		} else {
			rest = append(rest, a)
		}
	}
	filter := strings.Join(rest, " ")

	var c kube.Context
	var ns, title, body string
	switch {
	case piped:
		cur, err := kube.Current(a.cfg, "", "")
		if err != nil {
			return err
		}
		c, ns = cur, nsOr(cur.Namespace)
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			return errors.New("nothing came through the pipe")
		}
		what := "piped"
		if filter != "" {
			what = filter
		}
		title = "piped input"
		body = fmt.Sprintf("Output the engineer piped in (%s), from cluster context %s, namespace %s:\n\n```\n%s\n```\n",
			what, c.Name, ns, tailBytes(stripANSI(string(data)), 60000))
	default:
		cur, err := kube.Current(a.cfg, "", "")
		if err != nil {
			return err
		}
		c, ns = cur, nsOr(cur.Namespace)
		cl, err := a.client(a.src)
		if err != nil {
			return err
		}
		cl.Timeout = 20 * time.Second
		target, p, err := pickAskTarget(cl, c, filter)
		if err != nil || target == "" {
			return err
		}
		if target == "cluster" {
			fmt.Fprint(os.Stderr, tui.Dim.Render("gathering the cluster's problems… "))
			title = "cluster snapshot"
			body = clusterSnapshot(cl, c)
		} else {
			ns = p.Namespace
			fmt.Fprint(os.Stderr, tui.Dim.Render("gathering describe, events and logs… "))
			title = "pod " + p.Name
			body = gatherPod(cl, c.Name, ns, p.Name)
		}
	}

	file, err := a.writeDump(c, title, body)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, tui.Dim.Render(fmt.Sprintf("%d KB → %s", len(body)/1024, tildePath(file))))
	if dumpOnly {
		fmt.Println(file)
		return nil
	}

	if rex.Block() == "" {
		return a.asktail(c.Name, ns, file)
	}
	return rex.Split("ask "+c.Short, filepath.Join(a.binDir, "rex8s"), "asktail", c.Name, ns, file)
}

func nsOr(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

// claudeBin is plain `claude`: kask needs its permission prompts, so wrappers
// that skip them (ai_command, claude-auto) are not used.
func (a *app) claudeBin() (string, error) {
	switch strings.ToLower(strings.TrimSpace(a.cfg.AICommand)) {
	case "off", "none", "false":
		return "", errors.New("Claude is disabled (ai_command: off in " + a.cfg.File() + ")")
	}
	if p := pathx.Real("claude", a.binDir); p != "" {
		return p, nil
	}
	return "", errors.New("claude CLI not found in PATH")
}

func (a *app) dumpDir() string { return filepath.Join(a.cache, "dumps") }

func (a *app) writeDump(c kube.Context, title, body string) (string, error) {
	dir := a.dumpDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s.md", c.Short, time.Now().Format("20060102-150405"))
	prod := ""
	if c.Prod {
		prod = " (PRODUCTION)"
	}
	head := fmt.Sprintf("# %s · %s%s · %s\n\nCluster context: %s\n\n", title, c.Short, prod, time.Now().Format("2006-01-02 15:04"), c.Name)
	path := filepath.Join(dir, name)
	return path, os.WriteFile(path, []byte(head+body), 0o600)
}

// read-only commands Claude may run without asking
var askAllowed = []string{
	"Read",
	"Bash(kubectl get:*)", "Bash(kubectl describe:*)", "Bash(kubectl logs:*)", "Bash(kubectl top:*)",
	"Bash(kubectl events:*)", "Bash(kubectl explain:*)", "Bash(kubectl api-resources:*)",
	"Bash(kubectl config current-context:*)", "Bash(kubectl auth can-i:*)", "Bash(kubectl version:*)",
	"Bash(flux get:*)", "Bash(flux logs:*)", "Bash(flux tree:*)", "Bash(flux stats:*)",
}

const askSystemPrompt = `You are a senior Kubernetes SRE helping an engineer investigate a cluster from their terminal.
kubectl in this shell is already pointed at the cluster and namespace named in the context file.
Workloads are deployed with Flux (GitOps): fixes belong in the Git repository; manual changes are reverted on the next reconcile.
Read-only commands run without asking: write them with the subcommand first (kubectl get ... -n NS, kubectl logs POD -n NS --tail=200).
Anything that changes the cluster needs the engineer's approval. On a production cluster such a command is refused by a guard
unless prefixed with REX8S_FORCE=1 — only do that after the engineer explicitly agreed to that exact change.
Logs may contain secrets or personal data: never repeat tokens, passwords or keys back.
Be concise: diagnosis with evidence first, then the fix.`

// asktail runs the Claude conversation in its own pane, with kubectl scoped
// to the cluster and namespace like a rex8s pane.
func (a *app) asktail(ctxName, ns, file string) error {
	c, err := a.context(ctxName)
	if err != nil {
		return err
	}
	rex.Rename("ask " + c.Short)
	a.tint(c)
	bin, err := a.claudeBin()
	if err != nil {
		return err
	}
	id := strings.TrimPrefix(rex.Block(), "block:")
	if id == "" {
		id = fmt.Sprintf("pid%d", os.Getpid())
	}
	cfgPath := filepath.Join(a.cache, "ask-"+id+".yaml")
	if err := kube.WritePane(a.src, ctxName, ns, cfgPath); err != nil {
		return err
	}
	defer os.Remove(cfgPath)

	system := askSystemPrompt
	if lang := strings.TrimSpace(a.cfg.AILanguage); lang != "" {
		system += "\nAnswer in " + lang + "."
	}
	prod := ""
	if c.Prod {
		prod = " It is a PRODUCTION cluster."
	}
	prompt := fmt.Sprintf("%s has the context I collected from cluster %s (namespace %s).%s Read it and tell me what is wrong and how to fix it; "+
		"run read-only kubectl to dig further when the file isn't enough.", filepath.Base(file), c.Short, ns, prod)

	args := []string{"--append-system-prompt", system, "--allowedTools"}
	args = append(args, askAllowed...)
	args = append(args, "--", prompt)
	cmd := exec.Command(bin, args...)
	cmd.Dir = filepath.Dir(file)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Stdin = os.Stdin
	if !term.IsTerminal(int(os.Stdin.Fd())) { // kask run outside Rex with a pipe: talk on the terminal
		if tty, err := os.Open("/dev/tty"); err == nil {
			defer tty.Close()
			cmd.Stdin = tty
		}
	}
	cmd.Env = setEnv(os.Environ(), "KUBECONFIG", kube.PaneKubeconfig(cfgPath, a.src),
		"PATH", pathx.Prepend(a.binDir), "REX8S", "1", "REX8S_AGENT", "1")
	if err := cmd.Run(); err != nil {
		fmt.Fprint(os.Stderr, tui.Dim.Render("\nclaude ended ("+err.Error()+") · press enter to close"))
		bufio.NewReader(os.Stdin).ReadString('\n')
	}
	return nil
}

// pickAskTarget: the picker — first row the whole cluster, then every pod
// (broken first) with kfail's preview. Returns "cluster", "pod" or "" (esc).
func pickAskTarget(cl kube.Client, c kube.Context, filter string) (string, pod, error) {
	pods := map[string]pod{}
	it, err := tui.Run(tui.Options{
		Title: "Ask Claude about… (" + c.Short + ")",
		Query: filter,
		Load: func() ([]tui.Item, error) {
			out, err := cl.Get(c.Name, "get", "pods", "-A", "-o", "json")
			if err != nil {
				return nil, err
			}
			ps, err := parsePods(out)
			if err != nil {
				return nil, err
			}
			sort.SliceStable(ps, func(i, j int) bool { return failRank(ps[i]) < failRank(ps[j]) })
			broken, w := 0, 0
			for _, p := range ps {
				w = max(w, len(p.Namespace)+1+len(p.Name))
				if brokenLive(p) {
					broken++
				}
			}
			snap := "no broken pods"
			if broken > 0 {
				snap = fmt.Sprintf("%d broken", broken)
			}
			items := []tui.Item{{Key: "cluster", Label: fmt.Sprintf("%-*s", w, "★ the whole cluster"), Hint: snap}}
			for _, p := range ps {
				key := p.Namespace + "/" + p.Name
				pods[key] = p
				items = append(items, tui.Item{Key: key, Label: fmt.Sprintf("%-*s", w, key), Badge: p.badge(),
					Hint: fmt.Sprintf("%-18s %d/%d  restarts %-3d %s", p.status(), p.Ready, p.Total, p.Restarts, age(p.Created))})
			}
			return items, nil
		},
		PreviewHead: func(it tui.Item) string {
			if it.Key == "cluster" {
				return tui.Bold.Render("The whole cluster "+c.Short) + "\n\n" + tui.Dim.Render("Claude gets the kstatus summary, the broken pods with their last logs and the warnings of the last hour. Then you talk.")
			}
			return failHead(pods[it.Key])
		},
	})
	if err != nil || it == nil {
		return "", pod{}, err
	}
	if it.Key == "cluster" {
		return "cluster", pod{}, nil
	}
	return "pod", pods[it.Key], nil
}

// clusterSnapshot: kstatus's sections, then the details behind them — the
// broken pods with their last log lines and the last hour's warnings.
func clusterSnapshot(cl kube.Client, c kube.Context) string {
	var b strings.Builder
	parts := []func(kube.Client, string) section{statusNodes, statusPods, statusWorkloads, statusFlux, statusJobs, statusAlerts, statusCerts, statusWarnings}
	secs := make([]section, len(parts))
	var wg sync.WaitGroup
	for i, f := range parts {
		wg.Add(1)
		go func() { defer wg.Done(); secs[i] = f(cl, c.Name) }()
	}
	var podsOut, evOut []byte
	var podsErr, evErr error
	wg.Add(2)
	go func() { defer wg.Done(); podsOut, podsErr = cl.Get(c.Name, "get", "pods", "-A", "-o", "json") }()
	go func() {
		defer wg.Done()
		evOut, evErr = cl.Get(c.Name, "get", "events", "-A", "--field-selector", "type=Warning", "-o", "json")
	}()
	wg.Wait()

	b.WriteString("## Summary (kstatus)\n```\n")
	for _, s := range secs {
		if s.skip {
			continue
		}
		fmt.Fprintf(&b, "%-10s %s\n", s.name, stripANSI(strings.Join(s.lines, "\n           ")))
	}
	b.WriteString("```\n\n")

	if podsErr == nil {
		if ps, err := parsePods(podsOut); err == nil {
			var broken []pod
			for _, p := range failingPods(ps) {
				if brokenLive(p) {
					broken = append(broken, p)
				}
			}
			if len(broken) > 0 {
				b.WriteString("## Broken pods\n```\n")
				for _, p := range broken[:min(len(broken), 40)] {
					last := ""
					if p.LastReason != "" {
						last = "  last terminated: " + p.LastReason
					}
					fmt.Fprintf(&b, "%s/%s  %s  ready %d/%d  restarts %d  age %s%s\n",
						p.Namespace, p.Name, p.status(), p.Ready, p.Total, p.Restarts, age(p.Created), last)
				}
				if len(broken) > 40 {
					fmt.Fprintf(&b, "… %d more\n", len(broken)-40)
				}
				b.WriteString("```\n\n")
				// last log lines of the worst few (previous container when it restarted)
				for _, p := range broken[:min(len(broken), 5)] {
					args := []string{"logs", p.Name, "-n", p.Namespace, "--all-containers", "--prefix", "--tail=40"}
					if p.Restarts > 0 {
						args = append(args, "--previous")
					}
					out, err := cl.Get(c.Name, args...)
					if err != nil || strings.TrimSpace(string(out)) == "" {
						continue
					}
					fmt.Fprintf(&b, "### logs %s/%s (last 40 lines%s)\n```\n%s\n```\n\n", p.Namespace, p.Name,
						map[bool]string{true: ", previous container", false: ""}[p.Restarts > 0], tailBytes(string(out), 6000))
				}
			}
		}
	}
	if evErr == nil {
		if ws := recentWarnings(evOut, time.Hour); len(ws) > 0 {
			b.WriteString("## Warning events, last hour (one line per object and reason)\n```\n")
			for _, w := range ws[:min(len(ws), 60)] {
				b.WriteString(w + "\n")
			}
			b.WriteString("```\n")
		}
	}
	return b.String()
}

// recentWarnings: "ns kind/name reason (xN): message", newest first.
func recentWarnings(data []byte, within time.Duration) []string {
	var list struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			InvolvedObject struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"involvedObject"`
			Reason        string    `json:"reason"`
			Message       string    `json:"message"`
			Count         int       `json:"count"`
			LastTimestamp time.Time `json:"lastTimestamp"`
			EventTime     time.Time `json:"eventTime"`
		} `json:"items"`
	}
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	type w struct {
		when time.Time
		line string
	}
	var ws []w
	for _, e := range list.Items {
		when := e.LastTimestamp
		if when.IsZero() {
			when = e.EventTime
		}
		if time.Since(when) > within {
			continue
		}
		ws = append(ws, w{when, fmt.Sprintf("%s %s/%s %s (x%d): %s", e.Metadata.Namespace, e.InvolvedObject.Kind,
			e.InvolvedObject.Name, e.Reason, max(e.Count, 1), truncate(oneLine(e.Message), 300))})
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].when.After(ws[j].when) })
	out := make([]string, len(ws))
	for i, x := range ws {
		out[i] = x.line
	}
	return out
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// tailBytes keeps the end of s (the newest lines of a log).
func tailBytes(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return "…(earlier lines cut)…\n" + s
}
