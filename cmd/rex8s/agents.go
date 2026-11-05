package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vitalbh/rex8s/internal/tui"
)

// rex8s agents install|status|remove: a small skill in each coding agent's
// skills directory that says "run rex8s agent-guide". The guide itself ships
// in the binary, so upgrading rex8s updates it without rewriting the skill.

const skillMark = "<!-- Installed by \"rex8s agents install\". Remove it with \"rex8s agents remove\". -->"

const skillBody = `---
name: rex8s
description: Use when the user asks about their Kubernetes clusters or Flux GitOps — what is broken, what changed, which version runs where, logs, disk, resources, CronJobs, where a workload is defined in Git — or wants to change something on a cluster. rex8s gives read-only overviews across every cluster and a production guard for kubectl/helm/flux.
---

` + skillMark + `

Run ` + "`rex8s agent-guide`" + ` and follow the guide it prints. The guide comes from
the installed rex8s, so it matches that version.

If the command fails, rex8s is not installed: use kubectl directly, read-only,
and ask before changing anything.
`

type skillDir struct {
	agent string // claude, codex, cursor
	dir   string // …/skills/rex8s
	state string // current, outdated, missing, foreign
}

// agentSkillDirs: one per installed agent (its config directory exists).
func agentSkillDirs() []skillDir {
	home, _ := os.UserHomeDir()
	type base struct{ agent, dir string }
	bases := []base{{"claude", filepath.Join(home, ".claude")}} // Claude Code
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" && d != bases[0].dir {
		bases = append(bases, base{"claude", d}) // a second Claude Code profile
	}
	bases = append(bases,
		base{"codex", filepath.Join(home, ".agents")}, // Codex (and OpenCode, Pi…)
		base{"cursor", filepath.Join(home, ".cursor")},
	)
	var out []skillDir
	for _, bb := range bases {
		b := bb.dir
		if !fileExists(b) {
			continue
		}
		d := filepath.Join(b, "skills", "rex8s")
		s := skillDir{agent: bb.agent, dir: d, state: "missing"}
		if data, err := os.ReadFile(filepath.Join(d, "SKILL.md")); err == nil {
			switch {
			case string(data) == skillBody:
				s.state = "current"
			case strings.Contains(string(data), skillMark):
				s.state = "outdated"
			default:
				s.state = "foreign" // someone else's rex8s skill: left alone
			}
		}
		out = append(out, s)
	}
	return out
}

// rex8s agents install|status|remove [claude|codex|cursor ...] — every
// installed agent when none is named.
func (a *app) agents(args []string) error {
	cmd := "status"
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	only := map[string]bool{}
	for _, n := range args {
		switch n = strings.ToLower(n); n {
		case "all":
		case "claude", "codex", "cursor":
			only[n] = true
		default:
			return fmt.Errorf("unknown agent %q: claude, codex, cursor or all", n)
		}
	}
	var dirs []skillDir
	for _, s := range agentSkillDirs() {
		if len(only) == 0 || only[s.agent] {
			dirs = append(dirs, s)
		}
	}
	if len(dirs) == 0 {
		return fmt.Errorf("no such agent installed (~/.claude, ~/.agents for Codex, ~/.cursor)")
	}
	for _, s := range dirs {
		file := filepath.Join(s.dir, "SKILL.md")
		switch cmd {
		case "install":
			if s.state == "foreign" {
				fmt.Println(tui.Warn.Render("! ") + tildePath(file) + tui.Dim.Render(" is not rex8s's: left alone"))
				continue
			}
			if err := os.MkdirAll(s.dir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(file, []byte(skillBody), 0o644); err != nil {
				return err
			}
			fmt.Println(tui.Ok.Render("✓ ") + tildePath(file))
		case "remove":
			if s.state == "current" || s.state == "outdated" {
				_ = os.RemoveAll(s.dir)
				fmt.Println(tui.Ok.Render("✓ removed ") + tildePath(s.dir))
			}
		case "status":
			fmt.Printf("%-7s %-9s %s\n", s.agent, s.state, tildePath(file))
		default:
			return fmt.Errorf("usage: rex8s agents install|status|remove")
		}
	}
	return nil
}

// agentGuide is what agents read (rex8s agent-guide).
const agentGuide = `rex8s puts one Kubernetes cluster per Rex pane and adds overviews across every
cluster. Use it to answer questions about the user's clusters; prefer it over
long kubectl pipelines.

Clusters
- ` + "`rex8s list clusters`" + ` (JSON): every context with its short name and whether it is
  production. Name clusters to the user by short name.
- In a rex8s pane, kubectl already points at that pane's cluster and namespace.
  Elsewhere it uses the default context: pass --context explicitly.

Read-only commands (safe to run, plain text output; no colors when piped)
- What is broken: ` + "`rex8s list failing`" + ` (JSON, every cluster), ` + "`rex8s kstatus --context CTX`" + `.
- What changed: ` + "`rex8s kwhat [DURATION] [FILTER]`" + ` — rollouts with image changes, Flux applies,
  Helm upgrades, new commits; every cluster. Start here when something broke.
- Versions: ` + "`rex8s kver [-d] [FILTER]`" + ` — image tag per workload per cluster (-d: only differences).
- Disk: ` + "`rex8s kdisk [-A] [-a]`" + ` — PVC and node disk usage. Resources: ` + "`rex8s kreq [-n]`" + ` —
  OOM risk, over-provisioning, missing requests (needs metrics-server).
- CronJobs: ` + "`rex8s kcron [DURATION] [FILTER]`" + ` — upcoming runs and last results.
- Alerts: ` + "`rex8s list alerts`" + ` (JSON). Workloads: ` + "`rex8s list workloads`" + ` (JSON).
  Pods/services/Flux of a cluster: ` + "`rex8s list pods|services|flux CTX [NS]`" + ` (JSON).
- A pod's or the cluster's context for an investigation, as Markdown:
  ` + "`kubectl logs POD | rex8s kask --dump`" + ` prints the file path; ` + "`rex8s kincident --report`" + `.
- These commands use the current context (kdisk/kreq/kstatus) or every cluster
  (kwhat/kver/kcron/list); errors for a cluster with an expired login say so —
  tell the user to run ` + "`kauth CLUSTER`" + `.

Interactive commands (pickers: don't run them, suggest them to the user)
klogs, kgrep, kfail, kexec, kpf, knode, knet, kask, kflux, kjobs, krun, kroll,
kdiff, kjunk, kgit, ktint, khelp, kincident (it opens a Rex session).

Changing a cluster
- These clusters are deployed with Flux (GitOps): a manual change is reverted on
  the next reconcile. Prefer changing the Git repository; ` + "`kgit`" + ` (interactive) or the
  Kustomization's spec.path tells where.
- kubectl/helm/flux in this environment go through the rex8s guard. Run without a
  terminal (as an agent does), a command that changes a PRODUCTION cluster is
  refused with a message. Only after the user explicitly agrees to that exact
  change, run it again prefixed with REX8S_FORCE=1.
- Logs and describe output may contain secrets or personal data: don't repeat
  tokens, passwords or keys.

Settings
- Change rex8s settings with ` + "`rex8s config set KEY VALUE`" + ` (` + "`rex8s config`" + ` lists them);
  don't edit config.yaml by hand.
`

func (a *app) agentGuide() error {
	fmt.Print(agentGuide)
	return nil
}
