package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/vitalbh/rex8s/internal/desktop"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kview's actions: what a key does with the row under the cursor (or with
// every marked row: actionMany). They run in the background; reads go
// through kube.Client.Get, which waits for a refresh of the same context to
// finish first, so an action and a refresh never log in to one cluster at
// the same time.

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
		return k.warningAction(v, key)
	case kvRes:
		return k.resAction(v, key)
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
	if len(strings.TrimSpace(string(out))) == 0 {
		return tui.Result{Status: title + ": nothing"}
	}
	return tui.Result{Text: tui.Clean(string(out)), TextTitle: c.Short + " · " + title}
}

// kvObjects is what kview's y needs of kwatch.Hub: an object as its watch
// holds it.
type kvObjects interface {
	Object(c kube.Context, resource, ns, name string) (map[string]any, bool)
}

// yaml shows resource ns/name ("pods", "deployments", …) as YAML: from
// what the tab's watch holds, at once, when it has it; else kubectl get -o
// yaml. The watch dropped kubectl's last-applied annotation (managed
// fields kubectl hides too), so the title says where it came from.
func (k *kview) yaml(c kube.Context, title, resource, ns, name string) tui.Result {
	if k.objects != nil {
		if o, ok := k.objects.Object(c, resource, ns, name); ok {
			if out, err := yaml.Marshal(o); err == nil {
				return tui.Result{Text: tui.Clean(string(out)), TextTitle: c.Short + " · " + title + " (watched, without last-applied)"}
			}
		}
	}
	args := []string{"get", resource}
	if ns != "" {
		args = append(args, "-n", ns)
	}
	return k.text(c, title, append(args, name, "-o", "yaml")...)
}

// ── splits ─────────────────────────────────────────────────────────────

// kvSplits are the Rex splits kview opened. A second logs, shell or why
// replaces the split of the first (one per kind, not a new one each time);
// when kview ends, they all close.
type kvSplits struct {
	mu     sync.Mutex
	byKind map[string]string // kind → block ID of its split
	opened []string          // every split still open, for closing
}

// splitReplaces: the kinds whose split a new one replaces (an "ask"
// conversation is kept: each is its own).
var splitReplaces = map[string]bool{"logs": true, "shell": true, "why": true}

// split opens argv (a rex8s subcommand) in a Rex split next to this pane,
// focused or behind, in place of the split of the same kind opened before;
// outside Rex it runs here, and the browser comes back when it ends.
func (k *kview) split(kind string, focus bool, label string, argv ...string) tui.Result {
	self := filepath.Join(k.a.binDir, "rex8s")
	if rex.Block() == "" {
		return tui.Result{Exec: exec.Command(self, argv...)}
	}
	replaced, err := k.splits.open(kind, focus, label, append([]string{self}, argv...))
	if err != nil {
		return tui.Result{Err: err}
	}
	if replaced {
		return tui.Result{Status: label + " (in the " + kind + " split)"}
	}
	return tui.Result{Status: label + " opened in a split"}
}

// open splits the previous split of kind (it then closes, so the new one
// takes its place), or this pane when there is none or it is gone.
func (s *kvSplits) open(kind string, focus bool, label string, argv []string) (replaced bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.byKind[kind]
	var id string
	if prev != "" && splitReplaces[kind] {
		if id, err = rex.SplitBlock(prev, "right", 50, focus, label, argv...); err == nil {
			replaced = true
			closeBlocks(prev)
			s.forget(prev)
		}
	}
	if !replaced {
		if id, err = rex.SplitBlock(rex.Block(), "right", 50, focus, label, argv...); err != nil {
			return false, err
		}
	}
	if s.byKind == nil {
		s.byKind = map[string]string{}
	}
	s.byKind[kind] = id
	s.opened = append(s.opened, id)
	return replaced, nil
}

func (s *kvSplits) forget(id string) {
	for i, o := range s.opened {
		if o == id {
			s.opened = append(s.opened[:i], s.opened[i+1:]...)
			return
		}
	}
}

// closeAll closes every split still open (one the user closed is ignored).
func (s *kvSplits) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.opened) > 0 {
		closeBlocks(s.opened...)
	}
	s.opened, s.byKind = nil, nil
}

// closeBlocks closes Rex blocks, best effort.
func closeBlocks(ids ...string) {
	args := []string{"--timeout", "5s", "block", "close"}
	if s := rex.Session(); s != "" {
		args = append(args, "-s", s)
	}
	for _, id := range ids {
		_ = exec.Command(rex.Bin(), append(args, id)...).Run() // one at a time: a gone block fails alone
	}
}

// ── changes ────────────────────────────────────────────────────────────

// guarded runs a kubectl that changes something through the production
// guard (rex8s-guard kubectl, so the kubectl shim need not be installed;
// with guard: false it just runs kubectl), with the terminal (it asks for
// the cluster name on production), then waits for enter so its output can
// be read.
func (k *kview) guarded(c kube.Context, args ...string) tui.Result {
	bin := filepath.Join(k.a.binDir, "rex8s-guard")
	if _, err := os.Stat(bin); err != nil {
		return tui.Result{Err: errors.New("rex8s-guard is not next to rex8s (" + k.a.binDir + "): reinstall rex8s")}
	}
	script := `"$0" "$@"; printf '\n\033[2mpress enter to go back\033[0m'; read -r _`
	cmd := exec.Command("/bin/sh", append([]string{"-c", script, bin, "kubectl", "--context", c.Name}, args...)...)
	cmd.Env = setEnv(os.Environ(), "KUBECONFIG", k.a.src)
	return tui.Result{Exec: cmd}
}

// kvChange is one kubectl change of a many-object action.
type kvChange struct {
	c    kube.Context
	name string   // what it is, for the list: "pod default/web-7d9f on use (PROD)"
	args []string // kubectl arguments after --context
}

// actionMany runs a Multi action (delete, restart) on every marked row.
func (k *kview) actionMany(view int, its []tui.Item, key string) tui.Result {
	changes, verb, err := k.plan(its, key)
	if err != nil {
		return tui.Result{Err: err}
	}
	if len(changes) == 0 {
		return tui.Result{}
	}
	return tui.Result{Run: func() error { k.applyChanges(verb, changes); return nil }}
}

// plan is what key does to each marked row.
func (k *kview) plan(its []tui.Item, key string) (changes []kvChange, verb string, err error) {
	for _, it := range its {
		o, ok := k.obj(it.Key)
		if !ok {
			continue // gone since it was marked
		}
		var ch kvChange
		switch v := o.(type) {
		case kube.On[kube.Pod]:
			if key != "ctrl+d" {
				return nil, "", fmt.Errorf("%s does not take marked rows", key)
			}
			verb = "delete"
			ch = kvChange{v.C, it.Name, []string{"-n", v.V.Namespace, "delete", "pod", v.V.Name, "--wait=false"}}
		case kube.On[kube.Workload]:
			if key != "r" {
				return nil, "", fmt.Errorf("%s does not take marked rows", key)
			}
			verb = "restart"
			ch = kvChange{v.C, it.Name, []string{"-n", v.V.Namespace, "rollout", "restart", strings.ToLower(v.V.Kind) + "/" + v.V.Name}}
		case kvRes:
			if key != "ctrl+d" {
				return nil, "", fmt.Errorf("%s does not take marked rows", key)
			}
			verb = "delete"
			args := []string{"delete", v.res.Full(), v.name, "--wait=false"}
			if v.ns != "" {
				args = append([]string{"-n", v.ns}, args...)
			}
			ch = kvChange{v.c, it.Name, args}
		default:
			return nil, "", fmt.Errorf("%s does not take marked rows here", key)
		}
		changes = append(changes, ch)
	}
	return changes, verb, nil
}

// applyChanges runs with the terminal: the list, then the production
// guard's policy once for all of them (production asks for the clusters'
// names; agents are refused; kunlock lets them through), then each change.
func (k *kview) applyChanges(verb string, changes []kvChange) {
	w := k.out
	defer k.pause()
	var touched []kube.Context
	notes := make([]string, len(changes))
	for i, ch := range changes {
		touched = append(touched, ch.c)
		notes[i] = ch.name
	}
	fmt.Fprintf(w, "\n%s %d objects:\n", verb, len(changes))
	for _, n := range notes {
		fmt.Fprintln(w, "  "+n)
	}
	fmt.Fprintln(w)
	ok, err := k.a.confirmChange(touched, fmt.Sprintf("kview: %s %d object(s)", verb, len(changes)), notes...)
	if err != nil {
		fmt.Fprintln(w, tui.Err.Render(err.Error()))
		return
	}
	if !ok {
		return
	}
	failed := 0
	for _, ch := range changes {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		out, err := k.cl.Output(ctx, ch.c.Name, ch.args...)
		cancel()
		if err != nil {
			failed++
			fmt.Fprintln(w, tui.Err.Render("✗ "+ch.name+": "+lastLogLine(err.Error())))
			continue
		}
		fmt.Fprintln(w, tui.Ok.Render("✓ ")+strings.TrimSpace(string(out))+tui.Dim.Render(" ("+ch.c.Short+")"))
	}
	if failed > 0 {
		fmt.Fprintln(w, tui.Err.Render(fmt.Sprintf("%d of %d failed", failed, len(changes))))
	}
}

// pressEnter waits for enter on the terminal.
func pressEnter() {
	fmt.Fprint(os.Stderr, tui.Dim.Render("\npress enter to go back"))
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}

// ── pods ───────────────────────────────────────────────────────────────

// kvPickContainer asks which container of p when it has several (all: the
// first option is "all containers"), then runs then with it ("*" for all).
func kvPickContainer(p kube.Pod, what string, all bool, then func(container string) tui.Result) tui.Result {
	if len(p.Containers) < 2 {
		return then("")
	}
	opts := append([]string(nil), p.Containers...)
	if all {
		opts = append([]string{"all containers"}, opts...)
	}
	return tui.Result{Choose: &tui.Choice{Title: what + " of which container of " + p.Name + "?", Options: opts, Then: func(i int) tui.Result {
		if all {
			if i == 0 {
				return then("*")
			}
			i--
		}
		return then(p.Containers[i])
	}}}
}

func (k *kview) podAction(v kube.On[kube.Pod], key string) tui.Result {
	c, p := v.C, v.V
	switch key {
	case "enter":
		return k.podOwner(v)
	case "l":
		return kvPickContainer(p, "logs", true, func(ctr string) tui.Result {
			argv := []string{"_logs", c.Name, p.Namespace, p.Name}
			if ctr != "" {
				argv = append(argv, ctr)
			}
			return k.split("logs", false, "logs "+p.Name, argv...)
		})
	case "p":
		return kvPickContainer(p, "previous logs", false, func(ctr string) tui.Result {
			args := []string{"logs", "-n", p.Namespace, p.Name, "--previous", "--tail=1000"}
			title := "previous logs of " + p.Name
			if ctr != "" {
				args = append(args, "-c", ctr)
				title += " (" + ctr + ")"
			}
			return k.text(c, title, args...)
		})
	case "s":
		return kvPickContainer(p, "shell", false, func(ctr string) tui.Result {
			argv := []string{"_exec", c.Name, p.Namespace, p.Name}
			if ctr != "" {
				argv = append(argv, ctr)
			}
			return k.split("shell", true, "exec "+p.Name, argv...)
		})
	case "d":
		return k.text(c, "describe pod "+p.Name, "describe", "pod", "-n", p.Namespace, p.Name)
	case "e":
		return tui.Result{Text: objectEvents(k.cl, c.Name, p.Namespace, "Pod", p.Name, 0), TextTitle: c.Short + " · events of " + p.Name}
	case "y":
		return k.yaml(c, "pod "+p.Name, "pods", p.Namespace, p.Name)
	case "w":
		if _, err := k.a.aiCommand(); err != nil {
			return tui.Result{Err: err}
		}
		return k.split("why", false, "why "+p.Name, "_why", c.Name, p.Namespace, p.Name)
	case "a":
		return k.split("ask", true, "ask "+p.Name, "_askpod", c.Name, p.Namespace, p.Name)
	case "ctrl+d":
		return k.guarded(c, "-n", p.Namespace, "delete", "pod", p.Name)
	}
	return tui.Result{}
}

// ── following links (enter) ────────────────────────────────────────────

// where: " on CLUSTER" under -A, so a label says which cluster.
func (k *kview) where(c kube.Context) string {
	if k.multi {
		return " on " + c.Short
	}
	return ""
}

// podOwner goes to the pod's workload: a ReplicaSet's Deployment, a
// StatefulSet, DaemonSet, Job, or the node of a static pod.
func (k *kview) podOwner(v kube.On[kube.Pod]) tui.Result {
	c, p := v.C, v.V
	o := p.Owner
	if o.Kind == "ReplicaSet" {
		dep, err := k.replicaSetOwner(c, p)
		if err != nil {
			return tui.Result{Err: err}
		}
		if dep == "" {
			return k.gotoResource(c, "replicasets", p.Namespace, o.Name, "owner of pod "+p.Name)
		}
		o = kube.OwnerRef{Kind: "Deployment", Name: dep}
	}
	label := kube.ShortKind(o.Kind) + " " + p.Namespace + "/" + o.Name + " (owner of " + p.Name + ")" + k.where(c)
	switch o.Kind {
	case "":
		return tui.Result{Err: errors.New("pod " + p.Name + " has no owner")}
	case "Deployment", "StatefulSet", "DaemonSet":
		return tui.Result{Goto: vWorkloads + 1, Only: k.workloadIs(c, o.Kind, p.Namespace, o.Name), OnlyLabel: label}
	case "Job":
		return tui.Result{Goto: vJobs + 1, Only: k.jobIs(c, p.Namespace, o.Name), OnlyLabel: label}
	case "Node":
		return tui.Result{Goto: vNodes + 1, Only: k.nodeIs(c, o.Name), OnlyLabel: "node " + o.Name + " (of static pod " + p.Name + ")" + k.where(c)}
	}
	return k.gotoResource(c, strings.ToLower(o.Kind), p.Namespace, o.Name, "owner of pod "+p.Name)
}

// replicaSetOwner is the Deployment that owns the pod's ReplicaSet: its
// name less "-<pod-template-hash>", or else what the ReplicaSet says ("":
// a ReplicaSet of its own).
func (k *kview) replicaSetOwner(c kube.Context, p kube.Pod) (string, error) {
	if h := p.Labels["pod-template-hash"]; h != "" {
		if dep, ok := strings.CutSuffix(p.Owner.Name, "-"+h); ok && dep != "" {
			return dep, nil
		}
	}
	out, err := k.cl.Get(c.Name, "get", "replicaset", "-n", p.Namespace, p.Owner.Name, "-o", "json")
	if err != nil {
		return "", errors.New(lastLogLine(err.Error()))
	}
	var rs struct {
		Metadata struct {
			OwnerReferences []struct{ Kind, Name string } `json:"ownerReferences"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &rs); err != nil {
		return "", err
	}
	for _, o := range rs.Metadata.OwnerReferences {
		if o.Kind == "Deployment" {
			return o.Name, nil
		}
	}
	return "", nil
}

// gotoResource opens the ":KIND" tab on one object.
func (k *kview) gotoResource(c kube.Context, kind, ns, name, why string) tui.Result {
	r, ok := k.resolve(kind)
	if !ok {
		return tui.Result{Err: fmt.Errorf("no resource type %q to show", kind)}
	}
	if !r.Namespaced {
		ns = ""
	}
	only := func(it tui.Item) bool {
		o, ok := k.obj(it.Key)
		v, isRes := o.(kvRes)
		return ok && isRes && v.c.Name == c.Name && v.name == name && (ns == "" || v.ns == ns)
	}
	ref := name
	if ns != "" {
		ref = ns + "/" + name
	}
	return k.openResource(r, only, strings.ToLower(r.Kind)+" "+ref+" ("+why+")"+k.where(c))
}

func (k *kview) workloadIs(c kube.Context, kind, ns, name string) func(tui.Item) bool {
	return func(it tui.Item) bool {
		o, ok := k.obj(it.Key)
		w, isW := o.(kube.On[kube.Workload])
		return ok && isW && w.C.Name == c.Name && w.V.Kind == kind && w.V.Namespace == ns && w.V.Name == name
	}
}

func (k *kview) jobIs(c kube.Context, ns, name string) func(tui.Item) bool {
	return func(it tui.Item) bool {
		o, ok := k.obj(it.Key)
		j, isJ := o.(kvJob)
		if !ok || !isJ || j.c.Name != c.Name {
			return false
		}
		if j.job != nil {
			return j.job.Namespace == ns && j.job.Name == name
		}
		return j.cron.Namespace == ns && j.cron.Name == name
	}
}

func (k *kview) nodeIs(c kube.Context, name string) func(tui.Item) bool {
	return func(it tui.Item) bool {
		o, ok := k.obj(it.Key)
		n, isN := o.(kube.On[kube.Node])
		return ok && isN && n.C.Name == c.Name && n.V.Name == name
	}
}

func (k *kview) serviceIs(c kube.Context, ns, name string) func(tui.Item) bool {
	return func(it tui.Item) bool {
		o, ok := k.obj(it.Key)
		s, isS := o.(kube.On[kube.Service])
		return ok && isS && s.C.Name == c.Name && s.V.Namespace == ns && s.V.Name == name
	}
}

func (k *kview) podIs(c kube.Context, ns, name string) func(tui.Item) bool {
	return func(it tui.Item) bool {
		o, ok := k.obj(it.Key)
		p, isP := o.(kube.On[kube.Pod])
		return ok && isP && p.C.Name == c.Name && p.V.Namespace == ns && p.V.Name == name
	}
}

// podsOn accepts the pods scheduled on node name of cluster c.
func (k *kview) podsOn(c kube.Context, name string) func(tui.Item) bool {
	return func(it tui.Item) bool {
		o, ok := k.obj(it.Key)
		p, isP := o.(kube.On[kube.Pod])
		return ok && isP && p.C.Name == c.Name && p.V.Node == name
	}
}

// warningAction: enter goes to the object the warning is about (its tab,
// showing only it), d describes it.
func (k *kview) warningAction(v kvWarning, key string) tui.Result {
	if key == "d" {
		return k.text(v.c, "describe "+v.kind+"/"+v.name, "describe", "-n", v.ns, strings.ToLower(v.kind), v.name)
	}
	if key != "enter" {
		return tui.Result{}
	}
	ref := v.ns + "/" + v.name
	label := kube.ShortKind(v.kind) + " " + ref + " (from a warning)" + k.where(v.c)
	switch v.kind {
	case "Pod":
		return tui.Result{Goto: vPods + 1, Only: k.podIs(v.c, v.ns, v.name), OnlyLabel: label}
	case "Deployment", "StatefulSet", "DaemonSet":
		return tui.Result{Goto: vWorkloads + 1, Only: k.workloadIs(v.c, v.kind, v.ns, v.name), OnlyLabel: label}
	case "Service":
		return tui.Result{Goto: vServices + 1, Only: k.serviceIs(v.c, v.ns, v.name), OnlyLabel: label}
	case "Job", "CronJob":
		return tui.Result{Goto: vJobs + 1, Only: k.jobIs(v.c, v.ns, v.name), OnlyLabel: label}
	case "Node":
		return tui.Result{Goto: vNodes + 1, Only: k.nodeIs(v.c, v.name), OnlyLabel: "node " + v.name + " (from a warning)" + k.where(v.c)}
	}
	return k.gotoResource(v.c, strings.ToLower(v.kind), v.ns, v.name, "from a warning")
}

// ── workloads, services, jobs, nodes ───────────────────────────────────

func (k *kview) workloadAction(v kube.On[kube.Workload], key string) tui.Result {
	c, w := v.C, v.V
	ref := strings.ToLower(w.Kind) + "/" + w.Name
	switch key {
	case "enter": // its pods
		if w.Selector == "" {
			return tui.Result{Err: errors.New(ref + " has no matchLabels selector")}
		}
		label := "pods of " + kube.ShortKind(w.Kind) + " " + w.Namespace + "/" + w.Name + k.where(c)
		return tui.Result{Goto: vPods + 1, Only: k.podsOf(c, w.Namespace, w.Selector), OnlyLabel: label}
	case "l":
		if w.Selector == "" {
			return tui.Result{Err: errors.New(ref + " has no matchLabels selector")}
		}
		spec := logSpec{c.Name, w.Namespace, w.Selector, c.Short + " " + w.Name}
		return k.split("logs", false, "logs "+w.Name, "_wlogs", spec.String())
	case "r":
		return k.guarded(c, "-n", w.Namespace, "rollout", "restart", ref)
	case "d":
		return k.text(c, "describe "+ref, "describe", "-n", w.Namespace, ref)
	case "y":
		return k.yaml(c, ref, strings.ToLower(w.Kind)+"s", w.Namespace, w.Name)
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
	case "o": // its endpoints: the pods its selector picks
		if s.Selector == "" {
			return tui.Result{Err: errors.New("svc/" + s.Name + " has no selector: its endpoints are set by hand (d describes them)")}
		}
		return tui.Result{Goto: vPods + 1, Only: k.podsOf(c, s.Namespace, s.Selector), OnlyLabel: "pods of svc " + s.Namespace + "/" + s.Name + k.where(c)}
	case "d":
		return k.text(c, "describe svc "+s.Name, "describe", "svc", "-n", s.Namespace, s.Name)
	case "y":
		return k.yaml(c, "svc "+s.Name, "services", s.Namespace, s.Name)
	}
	return tui.Result{}
}

// jobAction: enter on a CronJob shows its runs (Hidden rows of the tab),
// on a Job its pods; l on a CronJob follows its last run's logs.
func (k *kview) jobAction(v kvJob, key string) tui.Result {
	c := v.c
	if v.cron != nil {
		cj := *v.cron
		switch key {
		case "enter":
			return tui.Result{Goto: vJobs + 1, Only: k.runsOf(c, cj.Namespace, cj.Name),
				OnlyLabel: "runs of cronjob " + cj.Namespace + "/" + cj.Name + k.where(c)}
		case "r":
			return k.guarded(c, "-n", cj.Namespace, "create", "job", manualJobName(cj.Name, time.Now()), "--from=cronjob/"+cj.Name)
		case "d":
			return k.text(c, "describe cronjob "+cj.Name, "describe", "cronjob", "-n", cj.Namespace, cj.Name)
		case "y":
			return k.yaml(c, "cronjob "+cj.Name, "cronjobs", cj.Namespace, cj.Name)
		case "l":
			if v.last == nil {
				return tui.Result{Err: errors.New("cronjob/" + cj.Name + " has no runs left (its history limits cleaned them up)")}
			}
			return k.split("logs", false, "logs "+v.last.Name, "_logs", c.Name, cj.Namespace, "job/"+v.last.Name)
		}
		return tui.Result{}
	}
	j := *v.job
	switch key {
	case "enter":
		return tui.Result{Goto: vPods + 1, Only: k.podsOf(c, j.Namespace, "job-name="+j.Name), OnlyLabel: "pods of job " + j.Namespace + "/" + j.Name + k.where(c)}
	case "l":
		return k.split("logs", false, "logs "+j.Name, "_logs", c.Name, j.Namespace, "job/"+j.Name)
	case "r":
		if j.Cron == "" {
			return tui.Result{Err: errors.New("job/" + j.Name + " is not from a CronJob: nothing to rerun")}
		}
		return k.guarded(c, "-n", j.Namespace, "create", "job", manualJobName(j.Cron, time.Now()), "--from=cronjob/"+j.Cron)
	case "d":
		return k.text(c, "describe job "+j.Name, "describe", "job", "-n", j.Namespace, j.Name)
	case "y":
		return k.yaml(c, "job "+j.Name, "jobs", j.Namespace, j.Name)
	}
	return tui.Result{}
}

func (k *kview) nodeAction(v kube.On[kube.Node], key string) tui.Result {
	c, n := v.C, v.V
	switch key {
	case "enter":
		return tui.Result{Goto: vPods + 1, Only: k.podsOn(c, n.Name), OnlyLabel: "pods on node " + n.Name + k.where(c)}
	case "s":
		return k.split("shell", true, "node "+n.Name, "_node", c.Name, n.Name)
	case "d":
		return k.text(c, "describe node "+n.Name, "describe", "node", n.Name)
	case "y":
		return k.yaml(c, "node "+n.Name, "nodes", "", n.Name)
	}
	return tui.Result{}
}
