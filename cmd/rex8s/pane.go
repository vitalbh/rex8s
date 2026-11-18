// The cluster pane: the picker tab, the isolated shell, the browser and
// events panes, kctx, kns and the pane label.

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

	"github.com/vitalbh/rex8s/internal/demo"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/pathx"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

func labelText(c kube.Context, ns string) string {
	if ns == "" {
		ns = c.Namespace
		if c.AllNS {
			ns = kube.AllNamespaces
		}
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

func (a *app) pick(mode string) error {
	var extra []tui.Item
	// demo mode never offers the real tabs to restore (screenshots, recordings)
	if lost := lostPanes(); len(lost) > 0 && !demo.Active() {
		extra = append(extra, tui.Item{Key: restoreKey, Label: fmt.Sprintf("restore %d tab(s)", len(lost)),
			Hint: restoreLabel(lost), Badge: "WAIT"})
	}
	// The picker runs in the new tab itself (Rex focuses a tab created by a
	// key binding; one created later from a floating picker's process would
	// stay in the background). The tab becomes the cluster pane or layout,
	// and closes if the user cancels.
	c, key, err := a.pickClusterOr("Kubernetes · pick a cluster ("+mode+")", "", extra)
	if key == restoreKey {
		err := a.restore()
		if err == nil {
			rex.CloseSelf()
		}
		return err
	}
	if err != nil {
		return err
	}
	if c == nil {
		rex.CloseSelf()
		return nil
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	if err := a.ensureLogin(cl, *c); err != nil {
		return err
	}
	ns, ok, err := a.pickNamespace("Namespace in "+c.Short, cl, c.Name, "", true)
	if err != nil {
		return err
	}
	if !ok {
		rex.CloseSelf()
		return nil
	}
	if rex.Block() == "" {
		// outside Rex: just open the shell here
		return a.pane(c.Name, ns, false)
	}
	if mode != "layout" {
		return a.pane(c.Name, ns, false)
	}
	// layout: this block becomes the browser (left: kview, or k9s); shell on the right, warnings below it
	self := filepath.Join(a.binDir, "rex8s")
	shellArgv := []string{self, "pane", "--layout", c.Name}
	warnArgv := []string{self, "_warnings", c.Name}
	if ns != "" {
		shellArgv = append(shellArgv, ns)
		warnArgv = append(warnArgv, ns)
	}
	shell, err := rex.SplitBlock(rex.Block(), "right", 40, false, "k8s", shellArgv...)
	if err != nil {
		return err
	}
	if _, err := rex.SplitBlock(shell, "below", 35, false, "warnings", warnArgv...); err != nil {
		return err
	}
	return a.tool("browser", c.Name, ns)
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
	env := setEnv(os.Environ(), "KUBECONFIG", kube.PaneKubeconfig(cfgPath, a.src), "PATH", pathx.Prepend(a.binDir), "REX8S", "1")
	if a.paneRun != "" { // kmulti CMD: run it first, then the shell stays
		fmt.Println(tui.Dim.Render("$ ") + a.paneRun)
		run := exec.Command(sh, "-c", a.paneRun)
		run.Stdin, run.Stdout, run.Stderr = os.Stdin, os.Stdout, os.Stderr
		run.Env = env
		_ = run.Run()
		fmt.Println()
	}
	cmd := exec.Command(sh, "-i")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env

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
	if which == "browser" && !a.cfg.UseK9s() { // kview, unless cluster_browser: k9s
		o := kviewOpts{ctxName: ctxName, ns: ns} // kview labels the pane: "kview PROD use/ns"
		if ns != "" && ns != kube.AllNamespaces {
			o.filter = ns + "/"
		}
		return a.runKview(o)
	}
	if which == "browser" {
		which = "k9s"
	}
	rex.Rename(which + " " + labelText(c, ns))
	a.tint(c)
	env := setEnv(os.Environ(), "KUBECONFIG", a.src)

	if which == "k9s" {
		bin := pathx.Real("k9s", a.binDir)
		if bin == "" {
			// no k9s: the layout still works, with the cluster's status here
			fmt.Println(tui.Dim.Render("k9s not installed (brew install k9s): showing kstatus instead"))
			return a.kstatus([]string{"-w", "--context", ctxName})
		}
		argv := []string{"k9s", "--context", ctxName}
		switch ns {
		case "":
		case kube.AllNamespaces:
			argv = append(argv, "-A")
		default:
			argv = append(argv, "-n", ns)
		}
		return syscall.Exec(bin, argv, env)
	}

	bin, err := a.kubectl()
	if err != nil {
		return err
	}
	argv := []string{"kubectl", "--context", ctxName, "get", "events", "-w"}
	if ns != "" && ns != kube.AllNamespaces {
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
		switch {
		case cur.AllNS:
			curNS = kube.AllNamespaces
		case curNS == "":
			curNS = "default"
		}
		picked, ok, err := a.pickNamespace("Namespace in "+cur.Short, cl, cur.Name, curNS, false)
		if err != nil || !ok {
			return err
		}
		ns = picked
	}
	if ns == "all" || ns == "-A" || ns == "--all-namespaces" {
		ns = kube.AllNamespaces
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
	recNS := c.Namespace
	if c.AllNS {
		recNS = kube.AllNamespaces
	}
	updateRecord(c.Name, c.Short, recNS)
	rex.Rename(l)
	rex.RenameWindow(l)
	a.tint(c)
	fmt.Println(l)
	return nil
}

// waitClose keeps a pane that is about to close open until enter, so its
// last lines (an error, the end of a stream) can be read. why, when given,
// leads the line: "logs ended · press enter to close".
func waitClose(why string) {
	msg := "press enter to close"
	if why != "" {
		msg = why + " · " + msg
	}
	fmt.Fprint(os.Stderr, tui.Dim.Render("\n"+msg))
	bufio.NewReader(os.Stdin).ReadString('\n')
}
