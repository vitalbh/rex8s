package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/vitalbh/rex8s/internal/guard"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kexec: pick a pod (and container) and open a shell in it, in a pane next
// to this one. kexec -A [FILTER]: a pod of any cluster.
func (a *app) kexec(args []string) error {
	filter := ""
	if len(args) > 0 && args[0] == "-A" {
		cp, _, err := a.pickPodAll("Shell · pods", false, strings.Join(args[1:], " "))
		if err != nil || cp == nil {
			return err
		}
		return a.shellIn(cp.c.Name, cp.p)
	}
	if len(args) > 0 {
		filter = args[0]
	}
	sel, err := a.pickPod("Shell", filter)
	if err != nil || sel == nil {
		return err
	}
	return a.shellIn(sel.ctx.Name, sel.pod)
}

// shellIn asks for the container when there are several, then opens the
// shell in a split (here outside Rex). Production asks first (exectail).
func (a *app) shellIn(ctxName string, p pod) error {
	container, ok, err := pickContainer(p, false)
	if err != nil || !ok {
		return err
	}
	if rex.Block() == "" {
		return a.exectail(ctxName, p.Namespace, p.Name, container)
	}
	argv := []string{filepath.Join(a.binDir, "rex8s"), "exectail", ctxName, p.Namespace, p.Name}
	if container != "" {
		argv = append(argv, container)
	}
	return rex.Split("exec "+p.Name, argv...)
}

// exectail runs `kubectl exec -it` (bash when the image has it, else sh). In
// production it asks for the same confirmation as mutating commands.
func (a *app) exectail(ctxName, ns, podName, container string) error {
	c, err := a.context(ctxName)
	if err != nil {
		return err
	}
	rex.Rename("exec " + c.Short + "/" + podName)
	a.tint(c)
	target := podName
	if container != "" {
		target += " -c " + container
	}
	if c.Prod && !guard.Confirm(c.Short, "kubectl exec -it "+target+" ("+ns+")") {
		return nil
	}
	bin, err := a.kubectl()
	if err != nil {
		return err
	}
	args := []string{"--context", ctxName, "-n", ns, "exec", "-it", podName}
	if container != "" {
		args = append(args, "-c", container)
	}
	args = append(args, "--", "sh", "-c", "if command -v bash >/dev/null 2>&1; then exec bash; else exec sh; fi")
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = setEnv(os.Environ(), "KUBECONFIG", a.src)
	if err := cmd.Run(); err != nil {
		fmt.Fprint(os.Stderr, tui.Dim.Render("\nshell ended ("+err.Error()+") · press enter to close"))
		bufio.NewReader(os.Stdin).ReadString('\n')
	}
	return nil
}
