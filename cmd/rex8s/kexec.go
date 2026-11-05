package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/vitalbh/rex8s/internal/guard"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kexec: pick a pod (and container) and open a shell in it, in a pane next
// to this one.
func (a *app) kexec(filter string) error {
	sel, err := a.pickPod("Shell", filter)
	if err != nil || sel == nil {
		return err
	}
	container, ok, err := pickContainer(sel.pod, false)
	if err != nil || !ok {
		return err
	}
	if rex.Block() == "" {
		return a.exectail(sel.ctx.Name, sel.ns, sel.pod.Name, container)
	}
	argv := []string{filepath.Join(a.binDir, "rex8s"), "exectail", sel.ctx.Name, sel.ns, sel.pod.Name}
	if container != "" {
		argv = append(argv, container)
	}
	return rex.Split("exec "+sel.pod.Name, argv...)
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
