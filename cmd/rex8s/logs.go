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

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

type pod struct {
	Namespace  string
	Name       string
	Phase      string
	Reason     string // e.g. CrashLoopBackOff
	LastReason string // why a container last terminated, e.g. OOMKilled
	Ready      int
	Total      int
	Restarts   int
	Created    time.Time
	Containers []string
}

func parsePods(data []byte) ([]pod, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string    `json:"name"`
				Namespace         string    `json:"namespace"`
				CreationTimestamp time.Time `json:"creationTimestamp"`
				DeletionTimestamp *string   `json:"deletionTimestamp"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Name string `json:"name"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				Phase             string `json:"phase"`
				Reason            string `json:"reason"`
				ContainerStatuses []struct {
					Ready        bool `json:"ready"`
					RestartCount int  `json:"restartCount"`
					State        struct {
						Waiting *struct {
							Reason string `json:"reason"`
						} `json:"waiting"`
						Terminated *struct {
							Reason string `json:"reason"`
						} `json:"terminated"`
					} `json:"state"`
					LastState struct {
						Terminated *struct {
							Reason string `json:"reason"`
						} `json:"terminated"`
					} `json:"lastState"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]pod, 0, len(list.Items))
	for _, it := range list.Items {
		p := pod{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, Phase: it.Status.Phase, Reason: it.Status.Reason,
			Created: it.Metadata.CreationTimestamp, Total: len(it.Spec.Containers)}
		if it.Metadata.DeletionTimestamp != nil {
			p.Reason = "Terminating"
		}
		for _, c := range it.Spec.Containers {
			p.Containers = append(p.Containers, c.Name)
		}
		for _, cs := range it.Status.ContainerStatuses {
			if cs.Ready {
				p.Ready++
			}
			p.Restarts += cs.RestartCount
			if p.Reason == "" && cs.State.Waiting != nil {
				p.Reason = cs.State.Waiting.Reason
			}
			if p.Reason == "" && cs.State.Terminated != nil && it.Status.Phase == "Running" {
				p.Reason = cs.State.Terminated.Reason
			}
			if p.LastReason == "" && cs.LastState.Terminated != nil {
				p.LastReason = cs.LastState.Terminated.Reason
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func (p pod) status() string {
	if p.Reason != "" {
		return p.Reason
	}
	return p.Phase
}

func (p pod) badge() string {
	switch {
	case p.Phase == "Succeeded":
		return "done"
	case p.Phase == "Failed" || strings.Contains(p.Reason, "BackOff") || strings.Contains(p.Reason, "Err") || p.Reason == "OOMKilled":
		return "FAIL"
	case p.Ready < p.Total || p.Phase != "Running":
		return "WAIT"
	}
	return "ok"
}

func age(t time.Time) string {
	d := time.Since(t)
	switch {
	case t.IsZero():
		return "?"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// klogs: pick a pod in the current namespace and follow its logs in a pane
// next to this one.
func (a *app) klogs(filter string) error {
	sel, err := a.pickPod("Logs", filter)
	if err != nil || sel == nil {
		return err
	}
	container, ok, err := pickContainer(sel.pod, true)
	if err != nil || !ok {
		return err
	}
	if rex.Block() == "" {
		return a.logtail(sel.ctx.Name, sel.ns, sel.pod.Name, container)
	}
	argv := []string{filepath.Join(a.binDir, "rex8s"), "logtail", sel.ctx.Name, sel.ns, sel.pod.Name}
	if container != "" {
		argv = append(argv, container)
	}
	return rex.Split("logs "+sel.pod.Name, argv...)
}

// podSelection is a pod picked in the current pane's cluster/namespace.
type podSelection struct {
	ctx kube.Context
	ns  string
	pod pod
	cl  kube.Client
}

// pickPod shows the pods of the current context's namespace (failing first).
func (a *app) pickPod(verb, filter string) (*podSelection, error) {
	c, err := kube.Current(a.cfg, "", "")
	if err != nil {
		return nil, err
	}
	ns := c.Namespace
	if ns == "" {
		ns = "default"
	}
	cl, err := a.client(a.src)
	if err != nil {
		return nil, err
	}
	pods := map[string]pod{}
	it, err := tui.Run(tui.Options{
		Title: fmt.Sprintf("%s · pods in %s/%s", verb, c.Short, ns),
		Query: filter,
		Load: func() ([]tui.Item, error) {
			out, err := cl.Get(c.Name, "get", "pods", "-n", ns, "-o", "json")
			if err != nil {
				return nil, err
			}
			ps, err := parsePods(out)
			if err != nil {
				return nil, err
			}
			if len(ps) == 0 {
				return nil, fmt.Errorf("no pods in %s", ns)
			}
			rank := map[string]int{"FAIL": 0, "WAIT": 1, "ok": 2, "done": 3}
			sort.SliceStable(ps, func(i, j int) bool { return rank[ps[i].badge()] < rank[ps[j].badge()] })
			w := 0
			for _, p := range ps {
				w = max(w, len(p.Name))
			}
			items := make([]tui.Item, 0, len(ps))
			for _, p := range ps {
				pods[p.Name] = p
				hint := fmt.Sprintf("%-18s %d/%d  restarts %-3d %s", p.status(), p.Ready, p.Total, p.Restarts, age(p.Created))
				items = append(items, tui.Item{Key: p.Name, Label: fmt.Sprintf("%-*s", w, p.Name), Hint: hint, Badge: p.badge()})
			}
			return items, nil
		},
	})
	if err != nil || it == nil {
		return nil, err
	}
	return &podSelection{ctx: c, ns: ns, pod: pods[it.Key], cl: cl}, nil
}

// pickContainer asks which container when the pod has several. allowAll adds
// "(all containers)", returned as "*". "" means the pod's only container.
func pickContainer(p pod, allowAll bool) (string, bool, error) {
	if len(p.Containers) <= 1 {
		return "", true, nil
	}
	var opts []tui.Item
	if allowAll {
		opts = append(opts, tui.Item{Key: "*", Label: "(all containers)", Dim: true})
	}
	for _, n := range p.Containers {
		opts = append(opts, tui.Item{Key: n, Label: n})
	}
	ch, err := tui.Run(tui.Options{Title: "Container in " + p.Name, Items: opts})
	if err != nil || ch == nil {
		return "", false, err
	}
	return ch.Key, true, nil
}

// logtail follows a pod's logs (runs in its own pane).
func (a *app) logtail(ctxName, ns, podName, container string) error {
	c, err := a.context(ctxName)
	if err != nil {
		return err
	}
	rex.Rename("logs " + c.Short + "/" + podName)
	a.tint(c)
	bin, err := a.kubectl()
	if err != nil {
		return err
	}
	args := []string{"--context", ctxName, "-n", ns, "logs", "-f", "--tail=300", podName}
	switch container {
	case "":
	case "*":
		args = append(args, "--all-containers", "--prefix")
	default:
		args = append(args, "-c", container)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = setEnv(os.Environ(), "KUBECONFIG", a.src)
	_ = cmd.Run()
	fmt.Fprint(os.Stderr, tui.Dim.Render("\nlogs ended · press enter to close"))
	bufio.NewReader(os.Stdin).ReadString('\n')
	return nil
}
