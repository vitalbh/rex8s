package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vitalbh/rex8s/internal/guard"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

const (
	nodeDebugNamespace = "default"
	nodeDebugImage     = "busybox:1.36"
)

type node struct {
	Name     string
	Ready    bool
	Pressure []string // MemoryPressure, DiskPressure, PIDPressure that are True
	Roles    []string
	Version  string
	Created  time.Time
	Unsched  bool
}

func parseNodes(data []byte) ([]node, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string            `json:"name"`
				CreationTimestamp time.Time         `json:"creationTimestamp"`
				Labels            map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Unschedulable bool `json:"unschedulable"`
			} `json:"spec"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
				NodeInfo struct {
					KubeletVersion string `json:"kubeletVersion"`
				} `json:"nodeInfo"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]node, 0, len(list.Items))
	for _, it := range list.Items {
		n := node{Name: it.Metadata.Name, Version: it.Status.NodeInfo.KubeletVersion,
			Created: it.Metadata.CreationTimestamp, Unsched: it.Spec.Unschedulable}
		for _, c := range it.Status.Conditions {
			switch {
			case c.Type == "Ready":
				n.Ready = c.Status == "True"
			case strings.HasSuffix(c.Type, "Pressure") && c.Status == "True":
				n.Pressure = append(n.Pressure, c.Type)
			}
		}
		for l := range it.Metadata.Labels {
			if r, ok := strings.CutPrefix(l, "node-role.kubernetes.io/"); ok && r != "" {
				n.Roles = append(n.Roles, r)
			}
		}
		sort.Strings(n.Roles)
		out = append(out, n)
	}
	// problems first, then by name
	sort.SliceStable(out, func(i, j int) bool {
		if bi, bj := out[i].badge() != "", out[j].badge() != ""; bi != bj {
			return bi
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (n node) badge() string {
	switch {
	case !n.Ready:
		return "DOWN"
	case len(n.Pressure) > 0:
		return "PRES"
	case n.Unsched:
		return "CORD"
	}
	return ""
}

func (n node) hint() string {
	parts := []string{}
	if !n.Ready {
		parts = append(parts, "NotReady")
	}
	parts = append(parts, n.Pressure...)
	if n.Unsched {
		parts = append(parts, "cordoned")
	}
	roles := "-"
	if len(n.Roles) > 0 {
		roles = strings.Join(n.Roles, ",")
	}
	parts = append(parts, roles, n.Version, age(n.Created))
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
			ns, err := parseNodes(out)
			if err != nil {
				return nil, err
			}
			w := 0
			for _, n := range ns {
				w = max(w, len(n.Name))
			}
			items := make([]tui.Item, 0, len(ns))
			for _, n := range ns {
				items = append(items, tui.Item{Key: n.Name, Label: fmt.Sprintf("%-*s", w, n.Name), Hint: n.hint(), Badge: n.badge()})
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
	return rex.Split("node "+it.Key, filepath.Join(a.binDir, "rex8s"), "nodetail", c.Name, it.Key)
}

// nodetail runs the node shell in its own pane. Production asks first.
func (a *app) nodetail(ctxName, nodeName string) error {
	c, err := a.context(ctxName)
	if err != nil {
		return err
	}
	rex.Rename("node " + c.Short + "/" + nodeName)
	a.tint(c)
	if c.Prod && !guard.Confirm(c.Short, "kubectl debug node/"+nodeName, "privileged pod on the node, with its filesystem at /host") {
		return nil
	}
	bin, err := a.kubectl()
	if err != nil {
		return err
	}
	image := a.cfg.NodeDebugImage
	if image == "" {
		image = nodeDebugImage
	}
	fmt.Println(tui.Dim.Render("the node's filesystem is at /host · chroot /host to use its tools · exit to leave"))
	start := time.Now()
	cmd := exec.Command(bin, "--context", ctxName, "-n", nodeDebugNamespace, "debug", "node/"+nodeName,
		"-it", "--profile=sysadmin", "--image="+image, "--", "sh")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = setEnv(os.Environ(), "KUBECONFIG", a.src)
	runErr := cmd.Run()

	// kubectl debug leaves the pod behind: remove the ones this run created
	k, _ := a.kubectl()
	if out, err := kubectlOut(k, a.src, 20*time.Second, "--context", ctxName, "-n", nodeDebugNamespace, "get", "pods", "-o", "json"); err == nil {
		if ps, err := parsePods([]byte(out)); err == nil {
			for _, p := range ps {
				if strings.HasPrefix(p.Name, "node-debugger-"+nodeName+"-") && p.Created.After(start.Add(-10*time.Second)) {
					if _, err := kubectlOut(k, a.src, 20*time.Second, "--context", ctxName, "-n", nodeDebugNamespace, "delete", "pod", p.Name, "--wait=false"); err == nil {
						fmt.Println(tui.Dim.Render("removed debug pod " + p.Name))
					}
				}
			}
		}
	}
	if runErr != nil {
		fmt.Fprint(os.Stderr, tui.Dim.Render("\nshell ended ("+runErr.Error()+") · press enter to close"))
		bufio.NewReader(os.Stdin).ReadString('\n')
	}
	return nil
}
