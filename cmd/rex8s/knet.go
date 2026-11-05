package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/vitalbh/rex8s/internal/guard"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

const netDebugImage = "nicolaka/netshoot:v0.13"

// knet: a throwaway pod with network tools (curl, dig, nc, tcpdump…) in this
// pane's namespace, in a split — to test what a pod there can reach. The
// pod is removed when the shell ends.
func (a *app) knet() error {
	c, err := kube.Current(a.cfg, "", "")
	if err != nil {
		return err
	}
	ns := nsOr(c.Namespace)
	if rex.Block() == "" {
		return a.nettail(c.Name, ns)
	}
	return rex.Split("net "+ns, filepath.Join(a.binDir, "rex8s"), "nettail", c.Name, ns)
}

func (a *app) nettail(ctxName, ns string) error {
	c, err := a.context(ctxName)
	if err != nil {
		return err
	}
	rex.Rename("net " + c.Short + "/" + ns)
	a.tint(c)
	image := a.cfg.NetDebugImage
	if image == "" {
		image = netDebugImage
	}
	if c.Prod && !guard.Confirm(c.Short, "kubectl run "+image+" in "+ns, "a temporary pod with network tools; removed when you exit") {
		return nil
	}
	bin, err := a.kubectl()
	if err != nil {
		return err
	}
	name := fmt.Sprintf("knet-%d", time.Now().Unix()%100000)
	fmt.Println(tui.Dim.Render(fmt.Sprintf("pod %s in %s · try: curl -sv http://SERVICE.%s:PORT · dig SERVICE · nc -vz HOST PORT · exit to leave", name, ns, ns)))
	cmd := exec.Command(bin, "--context", ctxName, "-n", ns, "run", name, "--rm", "-it", "--restart=Never",
		"--image="+image, "--labels=app.kubernetes.io/created-by=rex8s-knet", "--", "bash")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = setEnv(os.Environ(), "KUBECONFIG", a.src)
	runErr := cmd.Run()
	// --rm only works when kubectl ends cleanly: make sure the pod is gone
	k, _ := a.kubectl()
	_, _ = kubectlOut(k, a.src, 20*time.Second, "--context", ctxName, "-n", ns, "delete", "pod", name, "--ignore-not-found", "--wait=false")
	if runErr != nil {
		fmt.Fprint(os.Stderr, tui.Dim.Render("\nshell ended ("+runErr.Error()+") · press enter to close"))
		bufio.NewReader(os.Stdin).ReadString('\n')
	}
	return nil
}
