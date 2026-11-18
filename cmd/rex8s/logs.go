package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/logstream"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// pickPod shows the pods of the current context's namespace (failing first).
// Nil when cancelled.
func (a *app) pickPod(verb, filter string) (*kube.On[kube.Pod], error) {
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
	scope, where := []string{"-n", ns}, c.Short+"/"+ns
	if c.AllNS {
		scope, where = []string{"-A"}, c.Short+" (all namespaces)"
	}
	pods := map[string]kube.Pod{}
	it, err := tui.Run(tui.Options{
		Title: fmt.Sprintf("%s · pods in %s", verb, where),
		Query: filter,
		Load: func() ([]tui.Item, error) {
			out, err := cl.Get(c.Name, append([]string{"get", "pods", "-o", "json"}, scope...)...)
			if err != nil {
				return nil, err
			}
			ps, err := kube.ParsePods(out)
			if err != nil {
				return nil, err
			}
			if len(ps) == 0 {
				return nil, fmt.Errorf("no pods in %s", where)
			}
			rank := map[string]int{"FAIL": 0, "WAIT": 1, "ok": 2, "done": 3}
			sort.SliceStable(ps, func(i, j int) bool { return rank[ps[i].Badge()] < rank[ps[j].Badge()] })
			w := 0
			name := func(p kube.Pod) string {
				if c.AllNS {
					return p.Namespace + "/" + p.Name
				}
				return p.Name
			}
			for _, p := range ps {
				w = max(w, len(name(p)))
			}
			items := make([]tui.Item, 0, len(ps))
			for _, p := range ps {
				pods[name(p)] = p
				hint := fmt.Sprintf("%-18s %d/%d  restarts %-3d %s", p.Status(), p.Ready, p.Total, p.Restarts, kube.Age(p.Created))
				items = append(items, tui.Item{Key: name(p), Label: fmt.Sprintf("%-*s", w, name(p)), Hint: hint, Badge: p.Badge()})
			}
			return items, nil
		},
	})
	if err != nil || it == nil {
		return nil, err
	}
	return &kube.On[kube.Pod]{C: c, V: pods[it.Key]}, nil
}

// pickContainer asks which container when the pod has several. allowAll adds
// "(all containers)", returned as "*". "" means the pod's only container.
func pickContainer(p kube.Pod, allowAll bool) (string, bool, error) {
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

// logtail follows a pod's logs (runs in its own pane). podName may also be
// a kubectl reference such as job/NAME (kview and kjobs pass that for a
// Job): kubectl then picks the pod.
func (a *app) logtail(ctxName, ns, podName, container string, lo logOpts) error {
	re, err := lo.Compile()
	if err != nil {
		return err
	}
	c, err := a.context(ctxName)
	if err != nil {
		return err
	}
	kind, name := logRef(podName)
	rex.Rename("logs " + logTitle(c.Short, kind, name))
	if d := lo.Describe(); d != "" {
		fmt.Println(tui.Dim.Render(strings.TrimPrefix(d, " · ")))
	}
	a.tint(c)
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	// the pod writing the lines, for its localhost links: a job's pod when
	// it has exactly one running, otherwise none (pod/job/NAME is no pod)
	pod := podName
	if kind != "" {
		pod = ""
		if kind == "job" {
			if names, err := cl.RunningPods(ctxName, ns, "job-name="+name); err == nil && len(names) == 1 {
				pod = names[0]
			}
		}
	}
	window := "--tail=300"
	switch {
	case lo.Since != "":
		window = "--since=" + lo.Since
	case re != nil:
		window = "--tail=3000" // grep: look further back
	}
	args := []string{"-n", ns, "logs", "-f", window, podName}
	switch container {
	case "":
	case "*":
		args = append(args, "--all-containers", "--prefix")
	default:
		args = append(args, "-c", container)
	}
	cmd := cl.Command(context.Background(), ctxName, args...)
	cmd.Stderr = os.Stderr
	pipe, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, tui.Err.Render("cannot start kubectl: "+err.Error()))
	} else {
		containerLabel := func(_, c string) string { // only with --all-containers
			if c == "" {
				return ""
			}
			return tui.Dim.Render(c) + " "
		}
		f := &logstream.Follower{Grep: re}
		_ = f.Copy(pipe, a.logLinks(ctxName, ns), containerLabel, pod)
		_ = cmd.Wait()
	}
	fmt.Fprint(os.Stderr, tui.Dim.Render("\nlogs ended · press enter to close"))
	bufio.NewReader(os.Stdin).ReadString('\n')
	return nil
}

// logRef splits what logtail follows: "job/migrate" is ("job", "migrate"),
// a pod name is ("", name).
func logRef(s string) (kind, name string) {
	if k, n, ok := strings.Cut(s, "/"); ok {
		return k, n
	}
	return "", s
}

// logTitle names a log pane: "use/web-7d9f" for a pod, "use/migrate (job)"
// for a job.
func logTitle(short, kind, name string) string {
	if kind == "" {
		return short + "/" + name
	}
	return short + "/" + name + " (" + kind + ")"
}
