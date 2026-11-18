package main

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/guard"
	"github.com/vitalbh/rex8s/internal/logstream"
)

// command is one rex8s subcommand. The table below is the single list:
// dispatch, usage(), khelp, the k* wrappers setup installs and the panes
// that wait for enter after an error all come from it.
type command struct {
	name    string   // rex8s NAME
	aliases []string // other names that run it, not shown: old names, k9s
	wrapper string   // the k* command a rex8s pane has on PATH ("" none)
	args    string   // argument syntax, for usage and khelp
	short   string   // what it does, one line
	hidden  bool     // internal: run by Rex, Raycast or rex8s itself; not in usage
	pane    bool     // runs as a pane of its own: after an error, wait for enter
	noApp   bool     // runs before the config is loaded (demo)

	group   string      // khelp group of the wrapper's row ("" no row)
	details []string    // khelp preview of that row
	more    []helpEntry // further khelp rows (kpf -l, rex8s doctor…)

	run func(a *app, args []string) error
}

type helpEntry struct {
	group, cmd, usage, short string
	details                  []string
}

// khelp groups, in the order khelp lists them.
var helpGroups = []string{"pane", "pods", "cluster", "flux", "clusters"}

var (
	commands  []command
	byName    map[string]*command
	helpRows  []helpEntry
	wrapperOf map[string]string // wrapper → rex8s subcommand
)

func init() {
	commands = commandTable()
	byName = map[string]*command{}
	wrapperOf = map[string]string{}
	for i := range commands {
		c := &commands[i]
		byName[c.name] = c
		for _, al := range c.aliases {
			byName[al] = c
		}
		if c.wrapper != "" {
			byName[c.wrapper] = c // rex8s klogs = klogs = rex8s logs
			wrapperOf[c.wrapper] = c.name
		}
	}
	helpRows = buildHelp(commands)
}

// lookup finds a command by name, alias or wrapper.
func lookup(name string) *command { return byName[name] }

// arg is args[i], or "" when there are fewer.
func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

// need fails unless there are at least n arguments.
func need(name string, args []string, n int) error {
	if len(args) < n {
		return fmt.Errorf("%s: missing arguments (see rex8s help)", name)
	}
	return nil
}

func (a *app) dispatch(name string, args []string) error {
	c := lookup(name)
	if c == nil {
		usage()
		return fmt.Errorf("unknown command: %s", name)
	}
	return c.run(a, args)
}

// buildHelp: khelp's rows, by group, in table order inside a group.
func buildHelp(cs []command) []helpEntry {
	var rows []helpEntry
	for _, c := range cs {
		if c.group != "" {
			rows = append(rows, helpEntry{c.group, c.wrapper, strings.TrimSpace(c.wrapper + " " + c.args), c.short, c.details})
		}
		rows = append(rows, c.more...)
	}
	slices.SortStableFunc(rows, func(x, y helpEntry) int {
		return slices.Index(helpGroups, x.group) - slices.Index(helpGroups, y.group)
	})
	return rows
}

func usage() {
	var b strings.Builder
	b.WriteString("rex8s — one Kubernetes cluster per pane in Rex\n\n")
	line := func(syntax, short string) {
		const w = 34
		if len(syntax) > w {
			fmt.Fprintf(&b, "  rex8s %s\n  %-*s %s\n", syntax, w+6, "", short)
			return
		}
		fmt.Fprintf(&b, "  rex8s %-*s %s\n", w, syntax, short)
	}
	for _, c := range commands {
		if c.hidden {
			continue
		}
		short := c.short
		if c.wrapper != "" && c.wrapper != c.name {
			short += "  (" + c.wrapper + ")"
		}
		line(strings.TrimSpace(c.name+" "+c.args), short)
		for _, m := range c.more {
			if strings.HasPrefix(m.usage, c.wrapper+" ") && c.wrapper != "" {
				line(c.name+strings.TrimPrefix(m.usage, c.wrapper), m.short)
			}
		}
	}
	b.WriteString("\nIn a rex8s pane the k* commands run these without \"rex8s\" (klogs = rex8s logs);\nkhelp lists them in a picker.\n")
	b.WriteString("\nconfig: " + config.Path() + "\n")
	fmt.Fprint(os.Stderr, b.String())
}

// commandTable: every rex8s subcommand. Keep docs/commands.md in step
// (commands_test.go checks that every k* command is there).
func commandTable() []command {
	return []command{
		// ── the pane ────────────────────────────────────────────────────
		{name: "pick", args: "[tab|layout]", short: "cluster + namespace picker", pane: true,
			run: func(a *app, args []string) error {
				mode := arg(args, 0)
				if mode == "" {
					mode = "tab"
				}
				return a.pick(mode)
			}},
		{name: "pane", args: "CTX [NS]", short: "isolated shell on the cluster", pane: true,
			run: func(a *app, args []string) error {
				layout := false
				for len(args) > 0 && strings.HasPrefix(args[0], "--") {
					switch {
					case args[0] == "--layout":
						layout = true
					case args[0] == "--run" && len(args) > 1:
						a.paneRun = args[1]
						args = args[1:]
					}
					args = args[1:]
				}
				if err := need("pane", args, 1); err != nil {
					return err
				}
				return a.pane(args[0], arg(args, 1), layout)
			}},
		{name: "browser", aliases: []string{"k9s"}, args: "CTX [NS]", short: "kview on the cluster (k9s with cluster_browser: k9s)", pane: true,
			run: func(a *app, args []string) error {
				if err := need("browser", args, 1); err != nil {
					return err
				}
				return a.tool("browser", args[0], arg(args, 1))
			}},
		{name: "events", args: "CTX [NS]", short: "kubectl get events -w", pane: true,
			run: func(a *app, args []string) error {
				if err := need("events", args, 1); err != nil {
					return err
				}
				return a.tool("events", args[0], arg(args, 1))
			}},
		{name: "switch", wrapper: "kctx", args: "[CTX]", short: "switch this pane's cluster",
			group: "pane", details: []string{"No argument: picker. Only this pane changes; other tabs and ~/.kube/config stay."},
			run: func(a *app, args []string) error { return a.switchCtx(arg(args, 0)) }},
		{name: "ns", wrapper: "kns", args: "[NS|all]", short: "switch this pane's namespace",
			group: "pane", details: []string{"No argument: picker. kns all: every namespace — kubectl get/describe/top", "add -A by themselves, commands on one object ask for -n."},
			run: func(a *app, args []string) error { return a.setNS(arg(args, 0)) }},
		{name: "label", short: "refresh the pane label",
			run: func(a *app, args []string) error { return a.label() }},
		{name: "restore", short: "reopen the cluster tabs lost when the Rex server restarted",
			run: func(a *app, args []string) error { return a.restore() }},
		{name: "guard", args: "TOOL ARGS...", short: "confirm commands that mutate production",
			run: func(a *app, args []string) error {
				if err := need("guard", args, 1); err != nil {
					return err
				}
				return guard.Run(args[0], args[1:], a.binDir, a.cfg)
			}},
		{name: "kauth", wrapper: "kauth", args: "[CLUSTER]", short: "log in again when the credentials expired",
			group: "pane", details: []string{"Opens the login page and reads the pasted kubectl config commands (only those)."},
			run: func(a *app, args []string) error { return a.kauth(args) }},
		{name: "ktint", wrapper: "ktint", args: "[#hex|off] [--save]", short: "background color of production panes",
			group: "pane", details: []string{"No argument: presets, previewed live; enter saves to config.yaml.", "ktint #1f1430 tries a color in this pane; --save keeps it; ktint reset undoes."},
			run: func(a *app, args []string) error { return a.ktint(args) }},
		{name: "kunlock", wrapper: "kunlock", args: "[CLUSTER...|all] [DUR] · off · -l", short: "production changes go through without asking for a while (15m)",
			group: "pane", details: []string{"Like sudo's grace period, for an incident: default this pane's cluster for 15m.", "Agents get through too, but only you can unlock; kunlock off locks again; all logged."},
			run: func(a *app, args []string) error { return a.kunlock(args) }},
		{name: "khelp", wrapper: "khelp", args: "[FILTER]", short: "every command in a picker; enter runs it",
			run: func(a *app, args []string) error { return a.khelp(args) }},
		{name: "use-context", args: "CTX", short: "set the machine's default context (~/.kube/config)",
			run: func(a *app, args []string) error {
				if err := need("use-context", args, 1); err != nil {
					return err
				}
				return a.useContext(args[0])
			}},

		// ── pods ────────────────────────────────────────────────────────
		{name: "kview", wrapper: "kview", args: "[-A | --context CTX] [pods|workloads|services|jobs|nodes|warnings]", short: "browse the cluster like k9s: tabs, live lists, keys",
			group: "pods", details: []string{"tab or 1-6 switch, / filters, letters act: l logs, s shell, d describe, y yaml, r restart…", "-A: every cluster, each as it answers; R retries skipped ones. Changes ask y/N naming the object; production asks again."},
			run: func(a *app, args []string) error { return a.kview(args) }},
		{name: "kpods", wrapper: "kpods", args: "[failing] [FILTER]", short: "the pods of every cluster in one picker",
			group: "pods", details: []string{"Per cluster the ones that need attention first. failing: only those (kfail on every cluster).", "Preview: events and last logs. Enter: logs, why, ask, shell, describe, events."},
			run: func(a *app, args []string) error { return a.kpods(args) }},
		{name: "kfail", wrapper: "kfail", args: "[-n|-A] [FILTER]", short: "failing pods with events and last logs",
			group: "pods", details: []string{"All namespaces (-n: this one; -A: every cluster). Enter: logs, why, ask (kask), shell, describe, events."},
			run: func(a *app, args []string) error {
				if len(args) > 0 && args[0] == "-A" { // kpods failing
					return a.kpods(append([]string{"failing"}, args[1:]...))
				}
				return a.kfail(args)
			}},
		{name: "logs", wrapper: "klogs", args: "[-A] [-g RE] [-s DUR] [FILTER]", short: "follow a workload's or a pod's logs in a split",
			group: "pods", details: []string{"Workloads first: all their pods merged; new pods join after a rollout.", "klogs -A: workloads and pods of every cluster; mark several, one stream.", "-g REGEX keeps matching lines (highlighted); -s 1h starts an hour back.", "Cluster URLs (x.ns.svc…, a pod's http://0.0.0.0:PORT) are clickable: they port-forward on click."},
			run: func(a *app, args []string) error { return a.klogs(args) }},
		{name: "kgrep", wrapper: "kgrep", args: "[-A] [PATTERN]", short: "search the logs of a workload or pod",
			group: "pods", details: []string{"klogs with a filter: pick, then type what to look for (regex, case-insensitive).", "Keeps following: new matching lines keep coming. kgrep -A: several clusters."},
			run: func(a *app, args []string) error { return a.kgrep(args) }},
		{name: "kexec", wrapper: "kexec", args: "[-A] [FILTER]", short: "shell into a pod in a split",
			group: "pods", details: []string{"-A: a pod of any cluster (kexec -A api use goes straight in when one pod matches).", "Production asks first."},
			run: func(a *app, args []string) error { return a.kexec(args) }},
		{name: "kpf", wrapper: "kpf", args: "[-n|-A] [FILTER]", short: "port-forward a service in the background, print the URL",
			group: "pods", details: []string{"Every namespace, this pane's first (-n: only it; -A: every cluster). On a free local port; kubectl runs only while in use.", "kpf -A grafana starts it right away when only one service matches.", "kpf -l lists the running ones."},
			more: []helpEntry{
				{"pods", "kpf -f", "kpf NAME · kpf -f", "favorite port-forwards: start and open the browser", []string{"From config.yaml forwards (name, cluster, namespace, service, port, path).", "When it already runs, only the browser opens."}},
				{"pods", "kpf -l", "kpf -l · kpf --gc", "running port-forwards: open, copy, save, stop", []string{"Also in the Raycast Kube Context menu bar.", "kpf --gc removes what forwards that died left behind (their logs)."}},
			},
			run: func(a *app, args []string) error { return a.kpf(args) }},
		{name: "kwhy", wrapper: "kwhy", args: "[FILTER]", short: "ask Claude why a pod is unhealthy (one answer)",
			group: "pods", details: []string{"Describe (env values redacted), events and logs; Claude's tools are off."},
			run: func(a *app, args []string) error { return a.kwhy(arg(args, 0)) }},
		{name: "kask", wrapper: "kask", args: "[FILTER] · cmd | kask", short: "talk to Claude about the cluster or a pod",
			group: "pods", details: []string{"First row: the whole cluster. Read-only kubectl runs without asking;", "changes ask for approval, and production also needs REX8S_FORCE=1.", "kask --dump only saves the context file."},
			run: func(a *app, args []string) error { return a.kask(args) }},
		{name: "knode", wrapper: "knode", args: "[FILTER]", short: "shell on a node (kubectl debug node) in a split",
			group: "pods", details: []string{"Node filesystem at /host. Production asks first; the debug pod is removed on exit."},
			run: func(a *app, args []string) error { return a.knode(arg(args, 0)) }},
		{name: "knet", wrapper: "knet", short: "network toolbox pod (netshoot) in this namespace",
			group: "pods", details: []string{"curl, dig, nc, tcpdump… from inside the cluster. Production asks first; the pod is removed on exit."},
			run: func(a *app, args []string) error { return a.knet() }},

		// ── this cluster ────────────────────────────────────────────────
		{name: "kincident", wrapper: "kincident", args: "[CLUSTER] · --report · --ask", short: "a Rex session laid out for an incident",
			group: "cluster", details: []string{"kstatus -w, live warnings, recent changes (kwhat) and a shell on the cluster.", "--report: Markdown with the timeline, warnings and broken pods (postmortem).", "--ask: the same report, then a kask conversation about it."},
			run: func(a *app, args []string) error { return a.kincident(args) }},
		{name: "kstatus", wrapper: "kstatus", args: "[-w]", short: "the cluster's health on one screen",
			group: "cluster", details: []string{"Nodes, pods, workloads, Flux, jobs, alerts, certificates, warnings. -w refreshes."},
			run: func(a *app, args []string) error { return a.kstatus(args) }},
		{name: "kdisk", wrapper: "kdisk", args: "[-A] [-a] [FILTER]", short: "how full the volumes are (PVCs and node disks)",
			group: "cluster", details: []string{"Real usage from each node's kubelet, fullest first; above 70% by default (-a: all).", "-A: every cluster."},
			run: func(a *app, args []string) error { return a.kdisk(args) }},
		{name: "kreq", wrapper: "kreq", args: "[-n] [FILTER]", short: "requests and limits vs real usage",
			group: "cluster", details: []string{"Near the memory limit (OOM risk), over-provisioned (cost), no requests.", "This pane's cluster; usage is a kubectl top snapshot (needs metrics-server)."},
			run: func(a *app, args []string) error { return a.kreq(args) }},
		{name: "kwarn", wrapper: "kwarn", args: "[-n] | -A [CLUSTER...]", short: "stream Warning events only",
			group: "cluster", details: []string{"-A: every cluster (or the ones named) in one stream, each line led by its cluster."},
			run: func(a *app, args []string) error { return a.kwarn(args) }},
		{name: "kjobs", wrapper: "kjobs", args: "[-n|-A] [STATE] [FILTER]", short: "Jobs and CronJobs with logs and recent runs",
			group: "cluster", details: []string{"STATE: failed, running, done, suspended, cron. -A: every cluster. Enter: logs, run now, rerun, suspend/resume, delete."},
			run: func(a *app, args []string) error { return a.kjobs(args) }},

		// ── Flux ────────────────────────────────────────────────────────
		{name: "kgit", wrapper: "kgit", args: "[FILTER]", short: "where a workload is defined in Git: the file to change",
			group: "flux", details: []string{"Follows the Flux labels: HelmRelease → Kustomization → GitRepository.", "With a local clone (this directory or gitops_repos in the config) it finds the file; enter opens it in the editor or on GitHub."},
			run: func(a *app, args []string) error { return a.kgit(args) }},
		{name: "flux", wrapper: "kflux", short: "Flux resources, failing first: reconcile, suspend…",
			group: "flux",
			run:   func(a *app, args []string) error { return a.kflux() }},
		{name: "fdiff", wrapper: "fdiff", args: "[-A] [FILTER]", short: "what this working tree would change on the cluster",
			group: "flux", details: []string{"Run inside the GitOps repo. -A: every cluster."},
			run: func(a *app, args []string) error { return a.fdiff(args) }},
		{name: "fwatch", wrapper: "fwatch", args: "[-A] [--now]", short: "follow HEAD until Flux applied it and it rolled out",
			group: "flux", details: []string{"Run inside the GitOps repo after a push. Progress in the tab header."},
			run: func(a *app, args []string) error { return a.fwatch(args) }},
		{name: "monitor", short: "Flux dashboard + notifications, all clusters", pane: true,
			run: func(a *app, args []string) error { return a.monitor(args) }},

		// ── every cluster ───────────────────────────────────────────────
		{name: "kwhat", wrapper: "kwhat", args: "[DURATION] [FILTER]", short: "what changed on every cluster (default the last hour)",
			group: "clusters", details: []string{"Rollouts with the image change, what Flux applied, Helm upgrades, new commits.", "e.g. kwhat 6h api. Flux's part comes from events, kept about an hour."},
			run: func(a *app, args []string) error { return a.kwhat(args) }},
		{name: "kfind", wrapper: "kfind", args: "[-x] NAME", short: "find an object by name on every cluster",
			group: "clusters", details: []string{"Pods, workloads, services, ingresses, config maps, secrets (names only), jobs,", "cronjobs, PVCs, Flux Kustomizations and HelmReleases. A substring of the name; -x exact; NS/NAME works.", "Enter: a pod's actions, a workload's logs, a service's port-forward, describe, a shell there."},
			run: func(a *app, args []string) error { return a.kfind(args) }},
		{name: "kmulti", wrapper: "kmulti", args: "[GROUP] [-n NS|-A] [CMD...]", short: "a tab with one pane per cluster, side by side",
			group: "clusters", details: []string{"Pick clusters (space), or a group from the config (groups: {prod: [dal, sp]}).", "With CMD each pane runs it first (kmulti get pods -n api). Palette: Clusters Side by Side…"},
			run: func(a *app, args []string) error { return a.kmulti(args) }},
		{name: "kall", wrapper: "kall", args: "[CMD...]", short: "type CMD in every cluster pane of this tab",
			group: "clusters", details: []string{"In a kmulti tab: kall get pods -n api runs on every cluster at once.", "Production panes still ask before a change."},
			run: func(a *app, args []string) error { return a.kall(args) }},
		{name: "kjunk", wrapper: "kjunk", args: "[debug|pods|jobs|pv] [FILTER]", short: "leftovers on every cluster: mark and delete",
			group: "clusters", details: []string{"Ended debug pods (knode, knet), Failed/Evicted pods over a day old,", "Jobs finished over a week ago (not CronJob's), Released PVs. Production asks once (type the cluster names)."},
			run: func(a *app, args []string) error { return a.kjunk(args) }},
		{name: "kcron", wrapper: "kcron", args: "[DURATION] [FILTER]", short: "what CronJobs run next on every cluster (default 6h)",
			group: "clusters", details: []string{"One timeline, with how the last run went. Frequent jobs are folded.", "e.g. kcron 24h backup. Suspended ones are left out."},
			run: func(a *app, args []string) error { return a.kcron(args) }},
		{name: "kver", wrapper: "kver", args: "[-d] [FILTER]", short: "which version runs on which cluster",
			group: "clusters", details: []string{"-d: only the workloads whose versions differ."},
			run: func(a *app, args []string) error { return a.kver(args) }},
		{name: "kdiff", wrapper: "kdiff", args: "[FILTER]", short: "a workload's spec on two clusters",
			group: "clusters", details: []string{"Pick one row (same name elsewhere) or mark two with space."},
			run: func(a *app, args []string) error { return a.kdiff(args) }},
		{name: "krun", wrapper: "krun", args: "[-c CONTAINER] [FILTER] [-- CMD...]", short: "run a command in the pods of the workloads you mark",
			group: "clusters", details: []string{"e.g. krun nginx -- nginx -s reload. Production asks once (type the cluster names)."},
			run: func(a *app, args []string) error { return a.krun(args) }},
		{name: "kroll", wrapper: "kroll", args: "[FILTER]", short: "restart the workloads you mark, follow the rollouts",
			group: "clusters", details: []string{"Production asks once (type the cluster names)."},
			run: func(a *app, args []string) error { return a.kroll(args) }},

		// ── install and settings ────────────────────────────────────────
		{name: "setup", short: "install into Rex (binaries, k* commands, rex8s.lua)",
			run: func(a *app, args []string) error { return a.setup(args) }},
		{name: "doctor", short: "check the install, Rex, the tools and the config",
			more: []helpEntry{{"pane", "rex8s doctor", "rex8s doctor · setup · config · agents", "check and set up rex8s", []string{"doctor: checks the install, Rex, kubectl/flux/k9s/claude and the config.", "setup: (re)installs into Rex. config: settings. agents install: the skill for coding agents."}}},
			run:  func(a *app, args []string) error { return a.doctor(args) }},
		{name: "uninstall", short: "undo setup (the config stays)",
			run: func(a *app, args []string) error { return a.uninstall(args) }},
		{name: "config", args: "[get|set|unset|add|remove KEY [VALUE]]", short: "settings (no args: list)",
			run: func(a *app, args []string) error { return a.configCmd(args) }},
		{name: "agents", args: "install|status|remove", short: "the skill for coding agents",
			run: func(a *app, args []string) error { return a.agents(args) }},
		{name: "agent-guide", short: "print the guide coding agents follow",
			run: func(a *app, args []string) error { return a.agentGuide() }},
		{name: "demo", args: "on DIR | off", short: "show only the demo clusters (recordings)", noApp: true,
			run: func(_ *app, args []string) error { return demoSwitch(args) }},
		{name: "version", short: "print the version",
			run: func(a *app, args []string) error {
				fmt.Println(version)
				return nil
			}},
		{name: "help", aliases: []string{"-h", "--help"}, short: "this list",
			run: func(a *app, args []string) error {
				usage()
				return nil
			}},

		// ── for launchers (Raycast) ─────────────────────────────────────
		{name: "list", args: "KIND [CTX] [NS]", short: "JSON for launchers: clusters|namespaces|pods|services|flux|failing|pf|alerts|workloads|fwd",
			run: func(a *app, args []string) error { return a.list(args) }},
		{name: "freconcile", args: "CTX KIND NS NAME", short: "reconcile one Flux resource and wait (one line, for launchers)",
			run: func(a *app, args []string) error { return a.freconcile(args) }},

		// ── internal: run by rex/rex8s.lua, Raycast or rex8s itself ─────
		// One prefix, "_", and not in usage. The old names stay as aliases:
		// an older rex8s.lua, or a Rex layout saved before, still runs them.
		{name: "_fwd", aliases: []string{"fwd"}, args: "NAME", short: "start a favorite forward and open the browser (Rex action k8s_fwd)", hidden: true, pane: true,
			run: func(a *app, args []string) error {
				if err := need("_fwd", args, 1); err != nil {
					return err
				}
				return a.fwd(args[0])
			}},
		{name: "_warnings", aliases: []string{"warnings"}, args: "CTX [NS]", short: "the Warning events pane of the ⇧⌘K layout", hidden: true, pane: true,
			run: func(a *app, args []string) error {
				if err := need("_warnings", args, 1); err != nil {
					return err
				}
				return a.warnings(args[0], arg(args, 1))
			}},
		{name: "_logs", aliases: []string{"logtail"}, args: "[LOG FLAGS] CTX NS POD [CONTAINER]", short: "follow a pod's logs in this pane", hidden: true, pane: true,
			run: func(a *app, args []string) error {
				rest, lo := logstream.SplitFlags(args)
				if len(rest) < 3 {
					return need("_logs", rest, 3)
				}
				return a.logtail(rest[0], rest[1], rest[2], arg(rest, 3), lo)
			}},
		{name: "_wlogs", aliases: []string{"wlogtail"}, args: "[LOG FLAGS] SPEC...", short: "follow workloads' logs, merged, in this pane", hidden: true, pane: true,
			run: func(a *app, args []string) error {
				rest, lo := logstream.SplitFlags(args)
				return a.wlogtail(rest, lo)
			}},
		{name: "_exec", aliases: []string{"exectail"}, args: "CTX NS POD [CONTAINER]", short: "a shell in a pod, in this pane", hidden: true, pane: true,
			run: func(a *app, args []string) error {
				if err := need("_exec", args, 3); err != nil {
					return err
				}
				return a.exectail(args[0], args[1], args[2], arg(args, 3))
			}},
		{name: "_node", aliases: []string{"nodetail"}, args: "CTX NODE", short: "a shell on a node, in this pane", hidden: true, pane: true,
			run: func(a *app, args []string) error {
				if err := need("_node", args, 2); err != nil {
					return err
				}
				return a.nodetail(args[0], args[1])
			}},
		{name: "_net", aliases: []string{"nettail"}, args: "CTX NS", short: "the netshoot pod, in this pane", hidden: true, pane: true,
			run: func(a *app, args []string) error {
				if err := need("_net", args, 2); err != nil {
					return err
				}
				return a.nettail(args[0], args[1])
			}},
		{name: "_why", aliases: []string{"whytail"}, args: "CTX NS POD", short: "Claude's answer on why a pod is unhealthy, in this pane", hidden: true, pane: true,
			run: func(a *app, args []string) error {
				if err := need("_why", args, 3); err != nil {
					return err
				}
				return a.whytail(args[0], args[1], args[2])
			}},
		{name: "_ask", aliases: []string{"asktail"}, args: "CTX NS FILE", short: "a Claude conversation on a saved context, in this pane", hidden: true, pane: true,
			run: func(a *app, args []string) error {
				if err := need("_ask", args, 3); err != nil {
					return err
				}
				return a.asktail(args[0], args[1], args[2])
			}},
		{name: "_askpod", aliases: []string{"askpod"}, args: "CTX NS POD", short: "a Claude conversation about a pod, in this pane", hidden: true, pane: true,
			run: func(a *app, args []string) error {
				if err := need("_askpod", args, 3); err != nil {
					return err
				}
				return a.askpod(args[0], args[1], args[2])
			}},
		{name: "_describe", args: "CTX NS RESOURCE NAME", short: "kubectl describe of an object, in this pane (kfind)", hidden: true, pane: true,
			run: func(a *app, args []string) error {
				if err := need("_describe", args, 4); err != nil {
					return err
				}
				return a.describeTail(args[0], args[1], args[2], args[3])
			}},
		{name: "_pf", aliases: []string{"pfbg"}, args: "CTX NS SVC LOCAL:REMOTE KIND", short: "hold a port-forward in the background (started by kpf)", hidden: true,
			run: func(a *app, args []string) error { return a.pfbg(args) }},
		{name: "_pfopen", aliases: []string{"pfopen"}, args: "CTX NS SVC PORT", short: "port-forward a service and open it (Rex action k8s_pf)", hidden: true,
			run: func(a *app, args []string) error { return a.pfopen(args) }},
	}
}
