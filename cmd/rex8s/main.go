// rex8s — one Kubernetes cluster per pane in Rex.
//
//	rex8s pick [tab|layout]   cluster + namespace picker (floating popup)
//	rex8s pane CTX [NS]       isolated shell: KUBECONFIG scoped to this pane
//	rex8s k9s CTX [NS]        k9s
//	rex8s events CTX [NS]     kubectl get events -w
//	rex8s switch [CTX]        switch this pane's cluster (kctx)
//	rex8s ns [NS]             switch this pane's namespace (kns)
//	rex8s label               refresh the pane label
//	rex8s guard TOOL ARGS...  confirm commands that mutate production
//	rex8s fwatch [-A] [--now] follow a pushed commit until Flux applies it
//	rex8s flux                pick a Flux resource and act on it (kflux)
//	rex8s logs [FILTER]       pick a pod and follow its logs in a split (klogs)
//	rex8s monitor             Flux dashboard + notifications for every cluster
//	rex8s fdiff [-A] [FILTER] preview what the local repo would change (flux diff)
//	rex8s kpf [FILTER]        pick a service and port-forward it in a split
//	rex8s kauth [CLUSTER]     log in again when the credentials expired
package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/demo"
	"github.com/vitalbh/rex8s/internal/guard"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/pathx"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

var version = "dev"

type app struct {
	cfg    *config.Config
	binDir string // where rex8s and the wrappers live
	src    string // master kubeconfig
	cache  string // per-pane kubeconfigs
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	if cmd == "demo" {
		if err := demoSwitch(args); err != nil {
			fmt.Fprintln(os.Stderr, "rex8s: "+err.Error())
			os.Exit(1)
		}
		return
	}
	demo.Apply()
	a, err := newApp()
	if err == nil {
		err = a.dispatch(cmd, args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, tui.Err.Render("rex8s: "+err.Error()))
		if cmd == "pick" || cmd == "pane" || cmd == "k9s" || cmd == "events" || cmd == "logtail" || cmd == "pftail" || cmd == "exectail" || cmd == "warnings" || cmd == "whytail" || cmd == "monitor" {
			// runs in a pane that would close immediately: wait so the error can be read
			fmt.Fprint(os.Stderr, tui.Dim.Render("press enter to close"))
			bufio.NewReader(os.Stdin).ReadString('\n')
		}
		os.Exit(1)
	}
}

func newApp() (*app, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	binDir := filepath.Dir(exe)
	pathx.Fix(binDir)

	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	rex.Notifications = cfg.NotificationsOn()
	src := os.Getenv("REX8S_SOURCE")
	if src == "" {
		src = cfg.KubeconfigPath()
	}
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		h, _ := os.UserHomeDir()
		cache = filepath.Join(h, ".cache")
	}
	return &app{cfg: cfg, binDir: binDir, src: src, cache: filepath.Join(cache, "rex8s")}, nil
}

func (a *app) dispatch(cmd string, args []string) error {
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	need := func(n int) error {
		if len(args) < n {
			return fmt.Errorf("%s: missing arguments (see rex8s help)", cmd)
		}
		return nil
	}
	switch cmd {
	case "pick":
		mode := arg(0)
		if mode == "" {
			mode = "tab"
		}
		return a.pick(mode)
	case "pane":
		layout := len(args) > 0 && args[0] == "--layout"
		if layout {
			args = args[1:]
		}
		if err := need(1); err != nil {
			return err
		}
		return a.pane(args[0], arg(1), layout)
	case "restore":
		return a.restore()
	case "k9s", "events":
		if err := need(1); err != nil {
			return err
		}
		return a.tool(cmd, args[0], arg(1))
	case "switch":
		return a.switchCtx(arg(0))
	case "ns":
		return a.setNS(arg(0))
	case "label":
		return a.label()
	case "guard":
		if err := need(1); err != nil {
			return err
		}
		return a.guard(args[0], args[1:])
	case "fwatch":
		return a.fwatch(args)
	case "flux":
		return a.kflux()
	case "logs":
		return a.klogs(arg(0))
	case "logtail":
		if err := need(3); err != nil {
			return err
		}
		return a.logtail(args[0], args[1], args[2], arg(3))
	case "monitor":
		return a.monitor(args)
	case "fdiff":
		return a.fdiff(args)
	case "kpf":
		return a.kpf(arg(0))
	case "kauth":
		return a.kauth(args)
	case "list":
		return a.list(args)
	case "freconcile":
		return a.freconcile(args)
	case "kfail":
		return a.kfail(args)
	case "use-context":
		if err := need(1); err != nil {
			return err
		}
		return a.useContext(args[0])
	case "kexec":
		return a.kexec(arg(0))
	case "exectail":
		if err := need(3); err != nil {
			return err
		}
		return a.exectail(args[0], args[1], args[2], arg(3))
	case "kwarn":
		return a.kwarn(args)
	case "kwhy":
		return a.kwhy(arg(0))
	case "whytail":
		if err := need(3); err != nil {
			return err
		}
		return a.whytail(args[0], args[1], args[2])
	case "warnings":
		if err := need(1); err != nil {
			return err
		}
		return a.warnings(args[0], arg(1))
	case "pftail":
		if err := need(4); err != nil {
			return err
		}
		return a.pftail(args[0], args[1], args[2], args[3])
	case "version":
		fmt.Println(version)
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	}
	usage()
	return fmt.Errorf("unknown command: %s", cmd)
}

func usage() {
	fmt.Fprint(os.Stderr, `rex8s — one Kubernetes cluster per pane in Rex

  rex8s pick [tab|layout]   cluster + namespace picker
  rex8s pane CTX [NS]       isolated shell on the cluster
  rex8s k9s CTX [NS]        k9s
  rex8s events CTX [NS]     kubectl get events -w
  rex8s switch [CTX]        switch this pane's cluster   (kctx)
  rex8s ns [NS]             switch this pane's namespace (kns)
  rex8s label               refresh the pane label
  rex8s guard TOOL ARGS...  confirm commands that mutate production
  rex8s fwatch [-A] [--now] follow a pushed commit until Flux applies it  (fwatch)
  rex8s flux                pick a Flux resource and act on it          (kflux)
  rex8s logs [FILTER]       pick a pod, follow its logs in a split      (klogs)
  rex8s monitor             Flux dashboard + notifications, all clusters
  rex8s fdiff [-A] [FILTER] preview what the local repo would change    (fdiff)
  rex8s kpf [FILTER]        pick a service, port-forward it in a split  (kpf)
  rex8s kauth [CLUSTER]     log in again when the credentials expired   (kauth)
  rex8s kexec [FILTER]      pick a pod, open a shell in it in a split    (kexec)
  rex8s kwarn [-n]          stream Warning events only                    (kwarn)
  rex8s kwhy [FILTER]       pick a pod, ask Claude why it is unhealthy    (kwhy)
  rex8s kfail [-n] [FILTER] failing pods with their last logs             (kfail)
  rex8s list KIND [CTX] [NS] JSON for launchers: clusters|namespaces|pods|services
  rex8s use-context CTX     set the machine's default context (~/.kube/config)

config: `+config.Path()+"\n")
}

// ── helpers ────────────────────────────────────────────────────────────

func (a *app) kubectl() (string, error) {
	p := pathx.Real("kubectl", a.binDir)
	if p == "" {
		return "", errors.New("kubectl not found in PATH")
	}
	return p, nil
}

func (a *app) client(kubeconfig string) (kube.Client, error) {
	k, err := a.kubectl()
	return kube.Client{Kubectl: k, Kubeconfig: kubeconfig}, err
}

func (a *app) context(name string) (kube.Context, error) {
	raw, err := kube.Load(a.src)
	if err != nil {
		return kube.Context{}, err
	}
	c, ok := kube.Find(raw, a.cfg, name)
	if !ok {
		return kube.Context{}, fmt.Errorf("context %q not found in %s", name, a.src)
	}
	return c, nil
}

func labelText(c kube.Context, ns string) string {
	if ns == "" {
		ns = c.Namespace
	}
	if ns == "" {
		ns = "default"
	}
	if c.Prod {
		return "PROD " + c.Short + "/" + ns
	}
	return "k8s " + c.Short + "/" + ns
}

// cleanCache removes pane files left behind by panes that no longer exist
// (e.g. Rex was killed before the pane could clean up after itself).
func (a *app) cleanCache() {
	live, ok := rex.Blocks()
	files, _ := filepath.Glob(filepath.Join(a.cache, "*.yaml"))
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".yaml")
		if pid, isPID := strings.CutPrefix(id, "pid"); isPID {
			// pane opened outside Rex: gone when its process is gone
			if n, err := strconv.Atoi(pid); err == nil && syscall.Kill(n, 0) != nil {
				os.Remove(f)
			}
			continue
		}
		if ok && !live[id] {
			os.Remove(f)
		}
	}
}

// tint colors the pane background for production clusters (and resets it
// otherwise, e.g. after kctx from prod to dev).
func (a *app) tint(c kube.Context) {
	if c.Prod {
		rex.SetTheme(a.cfg.Tint())
	} else {
		rex.SetTheme("")
	}
}

// inPane returns the pane file (first entry of KUBECONFIG).
func (a *app) inPane() (string, error) {
	first := filepath.SplitList(os.Getenv("KUBECONFIG"))
	if len(first) == 0 || !strings.HasPrefix(first[0], a.cache+string(os.PathSeparator)) {
		return "", errors.New("only works inside a pane opened by rex8s")
	}
	return first[0], nil
}

func setEnv(env []string, kv ...string) []string {
	for i := 0; i+1 < len(kv); i += 2 {
		prefix := kv[i] + "="
		out := env[:0:0]
		for _, e := range env {
			if !strings.HasPrefix(e, prefix) {
				out = append(out, e)
			}
		}
		env = append(out, prefix+kv[i+1])
	}
	return env
}

// ── pickers ────────────────────────────────────────────────────────────

const restoreKey = "\x00restore"

func (a *app) pickCluster(title, selected string) (*kube.Context, error) {
	c, _, err := a.pickClusterOr(title, selected, nil)
	return c, err
}

// pickClusterOr is pickCluster with extra items on top; when one of them is
// chosen its Key is returned instead of a context.
func (a *app) pickClusterOr(title, selected string, extra []tui.Item) (*kube.Context, string, error) {
	raw, err := kube.Load(a.src)
	if err != nil {
		return nil, "", err
	}
	ctxs := kube.Contexts(raw, a.cfg)
	if len(ctxs) == 0 {
		return nil, "", fmt.Errorf("no visible contexts in %s", a.src)
	}
	cl, err := a.client(a.src)
	if err != nil {
		return nil, "", err
	}
	byKey := map[string]kube.Context{}
	extraHint := map[string]string{}
	items := make([]tui.Item, 0, len(ctxs)+len(extra))
	for _, e := range extra {
		extraHint[e.Key] = e.Hint
		items = append(items, e)
	}
	for _, c := range ctxs {
		byKey[c.Name] = c
		badge := "dev"
		if c.Prod {
			badge = "PROD"
		}
		items = append(items, tui.Item{Key: c.Name, Label: fmt.Sprintf("%-7s", c.Short), Hint: c.Name, Badge: badge})
	}
	it, err := tui.Run(tui.Options{
		Title:    title,
		Items:    items,
		Selected: selected,
		Prefetch: true,
		PreviewHead: func(it tui.Item) string {
			if h, ok := extraHint[it.Key]; ok {
				return tui.Bold.Render(it.Label) + "\n\n" + h
			}
			return clusterHead(byKey[it.Key])
		},
		PreviewBody: func(it tui.Item) string {
			if _, ok := extraHint[it.Key]; ok {
				return ""
			}
			return clusterBody(cl, byKey[it.Key])
		},
	})
	if err != nil || it == nil {
		return nil, "", err
	}
	if _, ok := extraHint[it.Key]; ok {
		return nil, it.Key, nil
	}
	c := byKey[it.Key]
	return &c, "", nil
}

func clusterHead(c kube.Context) string {
	tag := tui.Ok.Render("dev")
	if c.Prod {
		tag = tui.Prod.Render(" PROD ")
	}
	ns := c.Namespace
	if ns == "" {
		ns = "default"
	}
	server := strings.TrimPrefix(strings.TrimPrefix(c.Server, "https://"), "http://")
	row := func(k, v string) string { return tui.Dim.Render(fmt.Sprintf("%-10s", k)) + v }
	return strings.Join([]string{
		tui.Bold.Render(c.Short) + "  " + tag,
		"",
		row("context", c.Name),
		row("server", server),
		row("user", c.User),
		row("namespace", ns),
	}, "\n")
}

func clusterBody(cl kube.Client, c kube.Context) string {
	// Sequential on purpose: two concurrent calls against the same OIDC cluster
	// may refresh the token at the same time and invalidate the refresh token.
	// (Different clusters run in parallel: each item is a separate Cmd.)
	nss, nsErr := cl.Namespaces(c.Name)
	var flux kube.FluxStatus
	var fluxErr error
	if nsErr == nil {
		flux, fluxErr = cl.Flux(c.Name)
	}

	if nsErr != nil {
		if kube.AuthExpired(nsErr) {
			return tui.Warn.Render("login expired") + tui.Dim.Render(" · press enter to log in")
		}
		return accessError(c, nsErr)
	}
	row := func(k, v string) string { return tui.Dim.Render(fmt.Sprintf("%-11s", k)) + v }
	lines := []string{row("namespaces", fmt.Sprint(len(nss)))}

	switch {
	case fluxErr != nil:
		lines = append(lines, row("flux", tui.Err.Render(fluxErr.Error())))
	case !flux.Installed:
		lines = append(lines, row("flux", tui.Dim.Render("not installed")))
	default:
		parts := []string{tui.Ok.Render(fmt.Sprintf("%d ready", flux.Ready))}
		if flux.Failing > 0 {
			parts = append(parts, tui.Err.Render(fmt.Sprintf("%d failing", flux.Failing)))
		}
		if flux.Progressing > 0 {
			parts = append(parts, tui.Warn.Render(fmt.Sprintf("%d reconciling", flux.Progressing)))
		}
		if flux.Suspended > 0 {
			parts = append(parts, tui.Warn.Render(fmt.Sprintf("%d suspended", flux.Suspended)))
		}
		lines = append(lines, row("flux", strings.Join(parts, tui.Dim.Render(" · "))))
		for i, p := range flux.Problems {
			if i == 6 {
				lines = append(lines, tui.Dim.Render(fmt.Sprintf("  … and %d more", len(flux.Problems)-i)))
				break
			}
			st := tui.Err
			if p.Suspended {
				st = tui.Warn
			}
			lines = append(lines, "", st.Render(fmt.Sprintf("%s %s/%s", p.Kind, p.Namespace, p.Name)))
			if p.Message != "" && !p.Suspended {
				lines = append(lines, tui.Muted.Render("  "+truncate(oneLine(p.Message), 160)))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func (a *app) pickNamespace(title string, cl kube.Client, ctxName, current string, withDefault bool) (string, bool, error) {
	it, err := tui.Run(tui.Options{
		Title:    title,
		Selected: current,
		Load: func() ([]tui.Item, error) {
			var items []tui.Item
			if withDefault {
				items = append(items, tui.Item{Key: "", Label: "(context default)", Dim: true})
			}
			nss, err := cl.Namespaces(ctxName)
			if err != nil {
				if withDefault {
					items[0].Hint = "could not list namespaces: " + err.Error()
					return items, nil
				}
				return nil, err
			}
			for _, n := range nss {
				items = append(items, tui.Item{Key: n, Label: n})
			}
			return items, nil
		},
	})
	if err != nil || it == nil {
		return "", false, err
	}
	return it.Key, true, nil
}

// ── commands ───────────────────────────────────────────────────────────

func (a *app) pick(mode string) error {
	var extra []tui.Item
	if lost := lostPanes(); len(lost) > 0 {
		extra = append(extra, tui.Item{Key: restoreKey, Label: fmt.Sprintf("restore %d tab(s)", len(lost)),
			Hint: restoreLabel(lost), Badge: "WAIT"})
	}
	c, key, err := a.pickClusterOr("Kubernetes · pick a cluster ("+mode+")", "", extra)
	if key == restoreKey {
		return a.restore()
	}
	if err != nil || c == nil {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	if err := a.ensureLogin(cl, *c); err != nil {
		return err
	}
	ns, ok, err := a.pickNamespace("Namespace in "+c.Short, cl, c.Name, "", true)
	if err != nil || !ok {
		return err
	}
	args := map[string]any{"ctx": c.Name}
	if ns != "" {
		args["ns"] = ns
	}
	return rex.Do("k8s_"+mode, args)
}

func (a *app) pane(ctxName, ns string, layout bool) error {
	c, err := a.context(ctxName)
	if err != nil {
		return err
	}
	id := strings.TrimPrefix(rex.Block(), "block:")
	if id == "" {
		id = fmt.Sprintf("pid%d", os.Getpid())
	}
	a.cleanCache()
	cfgPath := filepath.Join(a.cache, id+".yaml")
	if err := kube.WritePane(a.src, ctxName, ns, cfgPath); err != nil {
		return err
	}
	defer os.Remove(cfgPath)

	rex.Rename(labelText(c, ns))
	rex.RenameWindow(labelText(c, ns))
	a.tint(c)
	if c.Prod {
		fmt.Println(tui.Prod.Render(" PRODUCTION: "+c.Short+" ") + "  mutating commands ask for confirmation (Flux reverts manual changes)")
	}
	fmt.Println(lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render(ctxName) +
		tui.Dim.Render("  ·  kns = switch namespace · kctx = switch cluster in this pane"))

	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/zsh"
	}
	cmd := exec.Command(sh, "-i")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = setEnv(os.Environ(), "KUBECONFIG", kube.PaneKubeconfig(cfgPath, a.src), "PATH", pathx.Prepend(a.binDir), "REX8S", "1")

	// the shell handles ctrl-c; we only wait for it to exit to remove the kubeconfig
	saveRecord(paneRecord{Block: rex.Block(), Context: ctxName, Short: c.Short, NS: ns, Layout: layout, Opened: time.Now()})

	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGHUP, syscall.SIGTERM)
	defer signal.Stop(sig)
	_ = cmd.Run()

	hangup := false // SIGHUP/SIGTERM: the tab was closed or Rex is going away
	for {
		select {
		case s := <-sig:
			if s == syscall.SIGHUP || s == syscall.SIGTERM {
				hangup = true
			}
			continue
		default:
		}
		break
	}
	forgetOnClose(rex.Block(), hangup)
	return nil
}

func (a *app) tool(which, ctxName, ns string) error {
	c, err := a.context(ctxName)
	if err != nil {
		return err
	}
	rex.Rename(which + " " + labelText(c, ns))
	a.tint(c)
	env := setEnv(os.Environ(), "KUBECONFIG", a.src)

	if which == "k9s" {
		bin := pathx.Real("k9s", a.binDir)
		if bin == "" {
			return errors.New("k9s not found in PATH")
		}
		argv := []string{"k9s", "--context", ctxName}
		if ns != "" {
			argv = append(argv, "-n", ns)
		}
		return syscall.Exec(bin, argv, env)
	}

	bin, err := a.kubectl()
	if err != nil {
		return err
	}
	argv := []string{"kubectl", "--context", ctxName, "get", "events", "-w"}
	if ns != "" {
		argv = append(argv, "-n", ns)
	} else {
		argv = append(argv, "-A")
	}
	return syscall.Exec(bin, argv, env)
}

func (a *app) switchCtx(ctxName string) error {
	pane, err := a.inPane()
	if err != nil {
		return err
	}
	if ctxName == "" {
		cur, _ := kube.Current(a.cfg, "", "")
		c, err := a.pickCluster("Switch this pane's cluster", cur.Name)
		if err != nil || c == nil {
			return err
		}
		cl, err := a.client(a.src)
		if err != nil {
			return err
		}
		if err := a.ensureLogin(cl, *c); err != nil {
			return err
		}
		ctxName = c.Name
	}
	if err := kube.WritePane(a.src, ctxName, "", pane); err != nil {
		return err
	}
	return a.label()
}

func (a *app) setNS(ns string) error {
	pane, err := a.inPane()
	if err != nil {
		return err
	}
	if ns == "" {
		cur, err := kube.Current(a.cfg, "", "")
		if err != nil {
			return err
		}
		cl, err := a.client(a.src)
		if err != nil {
			return err
		}
		curNS := cur.Namespace
		if curNS == "" {
			curNS = "default"
		}
		picked, ok, err := a.pickNamespace("Namespace in "+cur.Short, cl, cur.Name, curNS, false)
		if err != nil || !ok {
			return err
		}
		ns = picked
	}
	if err := kube.SetNamespace(pane, ns); err != nil {
		return err
	}
	return a.label()
}

func (a *app) label() error {
	c, err := kube.Current(a.cfg, "", "")
	if err != nil {
		return err
	}
	l := labelText(c, "")
	updateRecord(c.Name, c.Short, c.Namespace)
	rex.Rename(l)
	rex.RenameWindow(l)
	a.tint(c)
	fmt.Println(l)
	return nil
}

func (a *app) guard(tool string, args []string) error {
	return guard.Run(tool, args, a.binDir, a.cfg)
}

// ── text ────────────────────────────────────────────────────────────────

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
