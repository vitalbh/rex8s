package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vitalbh/rex8s/internal/desktop"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kview's actions: what a key does with the row under the cursor. They run
// in the background; reads go through kube.Client.Get, which waits for a
// refresh of the same context to finish first, so an action and a refresh
// never log in to one cluster at the same time.

func (k *kview) action(view int, it tui.Item, key string) tui.Result {
	o, ok := k.obj(it.Key)
	if !ok {
		return tui.Result{}
	}
	switch v := o.(type) {
	case kube.On[kube.Pod]:
		return k.podAction(v, key)
	case kube.On[kube.Workload]:
		return k.workloadAction(v, key)
	case kube.On[kube.Service]:
		return k.serviceAction(v, key)
	case kvJob:
		return k.jobAction(v, key)
	case kube.On[kube.Node]:
		return k.nodeAction(v, key)
	case kvWarning:
		return k.text(v.c, "describe "+v.kind+"/"+v.name, "describe", "-n", v.ns, strings.ToLower(v.kind), v.name)
	}
	return tui.Result{}
}

// text: a read-only kubectl's output in the viewer, cleaned of escape
// sequences (describe and YAML carry annotations, event messages, …).
func (k *kview) text(c kube.Context, title string, args ...string) tui.Result {
	out, err := k.cl.Get(c.Name, args...)
	if err != nil {
		return tui.Result{Err: errors.New(lastLogLine(err.Error()))}
	}
	return tui.Result{Text: tui.Clean(string(out)), TextTitle: c.Short + " · " + title}
}

// split opens argv (a rex8s subcommand) in a Rex split next to this pane,
// focused or behind; outside Rex it runs here, and the browser comes back
// when it ends.
func (k *kview) split(focus bool, label string, argv ...string) tui.Result {
	self := filepath.Join(k.a.binDir, "rex8s")
	if rex.Block() == "" {
		cmd := exec.Command(self, argv...)
		return tui.Result{Exec: cmd}
	}
	var err error
	if focus {
		err = rex.Split(label, append([]string{self}, argv...)...)
	} else {
		err = rex.SplitBehind(label, append([]string{self}, argv...)...)
	}
	if err != nil {
		return tui.Result{Err: err}
	}
	return tui.Result{Status: label + " opened in a split"}
}

// guarded runs a kubectl that changes something through the production
// guard, with the terminal (it asks for the cluster name on production),
// then waits for enter so its output can be read.
func (k *kview) guarded(c kube.Context, args ...string) tui.Result {
	bin := filepath.Join(k.a.binDir, "kubectl") // the guard wrapper
	if _, err := os.Stat(bin); err != nil {
		return tui.Result{Err: errors.New("the kubectl guard is not installed: rex8s setup")}
	}
	script := `"$0" "$@"; printf '\n\033[2mpress enter to go back\033[0m'; read -r _`
	cmd := exec.Command("/bin/sh", append([]string{"-c", script, bin, "--context", c.Name}, args...)...)
	cmd.Env = setEnv(os.Environ(), "KUBECONFIG", k.a.src)
	return tui.Result{Exec: cmd}
}

func (k *kview) podAction(v kube.On[kube.Pod], key string) tui.Result {
	c, p := v.C, v.V
	switch key {
	case "l":
		argv := []string{"logtail", c.Name, p.Namespace, p.Name}
		if len(p.Containers) > 1 {
			argv = append(argv, "*")
		}
		return k.split(false, "logs "+p.Name, argv...)
	case "s":
		argv := []string{"exectail", c.Name, p.Namespace, p.Name}
		status := ""
		if len(p.Containers) > 1 {
			argv = append(argv, p.Containers[0])
			status = " (container " + p.Containers[0] + ")"
		}
		r := k.split(true, "exec "+p.Name, argv...)
		if r.Status != "" {
			r.Status += status
		}
		return r
	case "d":
		return k.text(c, "describe pod "+p.Name, "describe", "pod", "-n", p.Namespace, p.Name)
	case "e":
		return tui.Result{Text: objectEvents(k.cl, c.Name, p.Namespace, "Pod", p.Name, 0), TextTitle: c.Short + " · events of " + p.Name}
	case "y":
		return k.text(c, "pod "+p.Name, "get", "pod", "-n", p.Namespace, p.Name, "-o", "yaml")
	case "w":
		if _, err := k.a.aiCommand(); err != nil {
			return tui.Result{Err: err}
		}
		return k.split(false, "why "+p.Name, "whytail", c.Name, p.Namespace, p.Name)
	case "a":
		return k.split(true, "ask "+p.Name, "askpod", c.Name, p.Namespace, p.Name)
	case "ctrl+d":
		return k.guarded(c, "-n", p.Namespace, "delete", "pod", p.Name)
	}
	return tui.Result{}
}

func (k *kview) workloadAction(v kube.On[kube.Workload], key string) tui.Result {
	c, w := v.C, v.V
	ref := strings.ToLower(w.Kind) + "/" + w.Name
	switch key {
	case "enter": // its pods
		if w.Selector == "" {
			return tui.Result{Err: errors.New(ref + " has no matchLabels selector")}
		}
		label := "pods of " + kube.ShortKind(w.Kind) + " " + w.Namespace + "/" + w.Name
		if k.multi {
			label += " on " + c.Short
		}
		return tui.Result{Goto: vPods + 1, Only: k.podsOf(c, w.Namespace, w.Selector), OnlyLabel: label}
	case "l":
		if w.Selector == "" {
			return tui.Result{Err: errors.New(ref + " has no matchLabels selector")}
		}
		spec := logSpec{c.Name, w.Namespace, w.Selector, c.Short + " " + w.Name}
		return k.split(false, "logs "+w.Name, "wlogtail", spec.String())
	case "r":
		return k.guarded(c, "-n", w.Namespace, "rollout", "restart", ref)
	case "d":
		return k.text(c, "describe "+ref, "describe", "-n", w.Namespace, ref)
	case "y":
		return k.text(c, ref, "get", "-n", w.Namespace, ref, "-o", "yaml")
	}
	return tui.Result{}
}

// podsOf accepts the pod rows of cluster c in namespace ns whose labels
// match selector ("k=v,k=v", a workload's matchLabels): the workload's own
// pods, not those of another one whose name starts the same.
func (k *kview) podsOf(c kube.Context, ns, selector string) func(tui.Item) bool {
	return func(it tui.Item) bool {
		o, ok := k.obj(it.Key)
		if !ok {
			return false
		}
		p, ok := o.(kube.On[kube.Pod])
		return ok && p.C.Name == c.Name && p.V.Namespace == ns && matchLabels(p.V.Labels, selector)
	}
}

// matchLabels: labels has every k=v of selector (an empty selector matches
// nothing).
func matchLabels(labels map[string]string, selector string) bool {
	if selector == "" {
		return false
	}
	for _, kv := range strings.Split(selector, ",") {
		key, val, _ := strings.Cut(kv, "=")
		if got, ok := labels[key]; !ok || got != val {
			return false
		}
	}
	return true
}

func (k *kview) serviceAction(v kube.On[kube.Service], key string) tui.Result {
	c, s := v.C, v.V
	switch key {
	case "enter":
		if len(s.Ports) == 0 {
			return tui.Result{Err: errors.New("svc/" + s.Name + " has no TCP port")}
		}
		port := s.Ports[0]
		for _, p := range s.Ports { // a web port first
			if detectKind(p).Web() {
				port = p
				break
			}
		}
		e, reused, err := k.a.lazyEnsure(c, s.Namespace, s.Name, port, 0)
		if err != nil {
			return tui.Result{Err: err}
		}
		kind := detectKind(port)
		url := kind.URL(e.Local)
		msg := "svc/" + s.Name + fmt.Sprintf(":%d → %s", port.Port, url)
		if reused {
			msg += " (already running)"
		}
		if cl := kind.Client(e.Local); cl != "" {
			msg += " · " + cl
		}
		if kind.Web() && desktop.CanOpen() {
			go desktop.OpenURL(url) // it waits for open: no zombie
		}
		return tui.Result{Status: msg}
	case "d":
		return k.text(c, "describe svc "+s.Name, "describe", "svc", "-n", s.Namespace, s.Name)
	case "y":
		return k.text(c, "svc "+s.Name, "get", "svc", "-n", s.Namespace, s.Name, "-o", "yaml")
	}
	return tui.Result{}
}

func (k *kview) jobAction(v kvJob, key string) tui.Result {
	c := v.c
	if v.cron != nil {
		cj := *v.cron
		switch key {
		case "r":
			return k.guarded(c, "-n", cj.Namespace, "create", "job", manualJobName(cj.Name, time.Now()), "--from=cronjob/"+cj.Name)
		case "d":
			return k.text(c, "describe cronjob "+cj.Name, "describe", "cronjob", "-n", cj.Namespace, cj.Name)
		case "y":
			return k.text(c, "cronjob "+cj.Name, "get", "cronjob", "-n", cj.Namespace, cj.Name, "-o", "yaml")
		case "l":
			return tui.Result{Err: errors.New("a CronJob has no logs: pick one of its jobs")}
		}
		return tui.Result{}
	}
	j := *v.job
	switch key {
	case "l":
		return k.split(false, "logs "+j.Name, "logtail", c.Name, j.Namespace, "job/"+j.Name)
	case "r":
		if j.Cron == "" {
			return tui.Result{Err: errors.New("job/" + j.Name + " is not from a CronJob: nothing to rerun")}
		}
		return k.guarded(c, "-n", j.Namespace, "create", "job", manualJobName(j.Cron, time.Now()), "--from=cronjob/"+j.Cron)
	case "d":
		return k.text(c, "describe job "+j.Name, "describe", "job", "-n", j.Namespace, j.Name)
	case "y":
		return k.text(c, "job "+j.Name, "get", "job", "-n", j.Namespace, j.Name, "-o", "yaml")
	}
	return tui.Result{}
}

func (k *kview) nodeAction(v kube.On[kube.Node], key string) tui.Result {
	c, n := v.C, v.V
	switch key {
	case "s":
		return k.split(true, "node "+n.Name, "nodetail", c.Name, n.Name)
	case "d":
		return k.text(c, "describe node "+n.Name, "describe", "node", n.Name)
	case "y":
		return k.text(c, "node "+n.Name, "get", "node", n.Name, "-o", "yaml")
	}
	return tui.Result{}
}
