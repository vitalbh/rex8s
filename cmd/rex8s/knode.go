package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

const (
	nodeDebugNamespace = "default"
	nodeDebugImage     = "busybox:1.36"
)

// sortNodes: problems first, then by name.
func sortNodes(ns []kube.Node) {
	sort.SliceStable(ns, func(i, j int) bool {
		if bi, bj := nodeBadge(ns[i]) != "", nodeBadge(ns[j]) != ""; bi != bj {
			return bi
		}
		return ns[i].Name < ns[j].Name
	})
}

func nodeBadge(n kube.Node) string {
	switch {
	case !n.Ready:
		return "DOWN"
	case len(n.Pressure) > 0:
		return "PRES"
	case n.Unschedulable:
		return "CORD"
	}
	return ""
}

func nodeHint(n kube.Node) string {
	parts := []string{}
	if !n.Ready {
		parts = append(parts, "NotReady")
	}
	parts = append(parts, n.Pressure...)
	if n.Unschedulable {
		parts = append(parts, "cordoned")
	}
	roles := "-"
	if len(n.Roles) > 0 {
		roles = strings.Join(n.Roles, ",")
	}
	parts = append(parts, roles, n.Version, kube.Age(n.Created))
	return strings.Join(parts, "  ")
}

// knode: pick a node of this pane's cluster and open a shell on it (kubectl
// debug node, a privileged pod with the node's filesystem at /host) in a
// split. The debug pod is removed when the shell ends.
func (a *app) knode(filter string) error {
	c, err := kube.Current(a.cfg, "", "")
	if err != nil {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	it, err := tui.Run(tui.Options{
		Title: "Shell on a node of " + c.Short,
		Query: filter,
		Load: func() ([]tui.Item, error) {
			out, err := cl.Get(c.Name, "get", "nodes", "-o", "json")
			if err != nil {
				return nil, err
			}
			ns, err := kube.ParseNodes(out)
			if err != nil {
				return nil, err
			}
			sortNodes(ns)
			w := 0
			for _, n := range ns {
				w = max(w, len(n.Name))
			}
			items := make([]tui.Item, 0, len(ns))
			for _, n := range ns {
				items = append(items, tui.Item{Key: n.Name, Label: fmt.Sprintf("%-*s", w, n.Name), Hint: nodeHint(n), Badge: nodeBadge(n)})
			}
			return items, nil
		},
	})
	if err != nil || it == nil {
		return err
	}
	if rex.Block() == "" {
		return a.nodetail(c.Name, it.Key)
	}
	return rex.Split("node "+it.Key, filepath.Join(a.binDir, "rex8s"), "_node", c.Name, it.Key)
}

// nodetail runs the node shell in its own pane. Production asks first.
func (a *app) nodetail(ctxName, nodeName string) error {
	c, err := a.context(ctxName)
	if err != nil {
		return err
	}
	rex.Rename("node " + c.Short + "/" + nodeName)
	a.tint(c)
	if ok, err := a.confirmChange([]kube.Context{c}, "knode: kubectl debug node/"+nodeName, "privileged pod on the node, with its filesystem at /host"); !ok {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	image := a.cfg.NodeDebugImage
	if image == "" {
		image = nodeDebugImage
	}
	fmt.Println(tui.Dim.Render("the node's filesystem is at /host · chroot /host to use its tools · exit to leave"))
	start := time.Now()
	runErr := cl.Run(ctxName, "-n", nodeDebugNamespace, "debug", "node/"+nodeName,
		"-it", "--profile=sysadmin", "--image="+image, "--", "sh")

	// kubectl debug leaves the pod behind: remove the ones this run created
	cl.Timeout = 20 * time.Second
	if out, err := cl.Get(ctxName, "-n", nodeDebugNamespace, "get", "pods", "-o", "json"); err == nil {
		if ps, err := kube.ParsePods(out); err == nil {
			for _, p := range ps {
				if strings.HasPrefix(p.Name, "node-debugger-"+nodeName+"-") && p.Created.After(start.Add(-10*time.Second)) {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					_, err := cl.Output(ctx, ctxName, "-n", nodeDebugNamespace, "delete", "pod", p.Name, "--wait=false")
					cancel()
					if err == nil {
						fmt.Println(tui.Dim.Render("removed debug pod " + p.Name))
					}
				}
			}
		}
	}
	if runErr != nil {
		waitClose("shell ended (" + runErr.Error() + ")")
	}
	return nil
}
