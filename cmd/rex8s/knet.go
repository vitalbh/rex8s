package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

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
	return rex.Split("net "+ns, filepath.Join(a.binDir, "rex8s"), "_net", c.Name, ns)
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
	if ok, err := a.confirmChange([]kube.Context{c}, "knet: kubectl run "+image+" in "+ns, "a temporary pod with network tools; removed when you exit"); !ok {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("knet-%d", time.Now().Unix()%100000)
	fmt.Println(tui.Dim.Render(fmt.Sprintf("pod %s in %s · try: curl -sv http://SERVICE.%s:PORT · dig SERVICE · nc -vz HOST PORT · exit to leave", name, ns, ns)))
	runErr := cl.Run(ctxName, "-n", ns, "run", name, "--rm", "-it", "--restart=Never",
		"--image="+image, "--labels=app.kubernetes.io/created-by=rex8s-knet", "--", "bash")
	// --rm only works when kubectl ends cleanly: make sure the pod is gone
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, _ = cl.Output(ctx, ctxName, "-n", ns, "delete", "pod", name, "--ignore-not-found", "--wait=false")
	if runErr != nil {
		waitClose("shell ended (" + runErr.Error() + ")")
	}
	return nil
}
