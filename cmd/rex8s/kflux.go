package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kflux: pick a Kustomization/HelmRelease on the current cluster (failing
// first) and act on it.
func (a *app) kflux() error {
	c, err := kube.Current(a.cfg, "", "")
	if err != nil {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}

	byKey := map[string]kube.FluxItem{}
	it, err := tui.Run(tui.Options{
		Title: "Flux on " + c.Short,
		Load: func() ([]tui.Item, error) {
			items, installed, err := cl.FluxItems(c.Name)
			if err != nil {
				return nil, err
			}
			if !installed {
				return nil, errors.New("flux is not installed on " + c.Short)
			}
			out := make([]tui.Item, 0, len(items))
			for _, f := range items {
				byKey[f.Key()] = f
				hint := ""
				if f.Ready != "True" && f.Message != "" {
					hint = truncate(oneLine(f.Message), 70)
				}
				out = append(out, tui.Item{
					Key:   f.Key(),
					Label: fmt.Sprintf("%-14s %s/%s", f.Kind, f.Namespace, f.Name),
					Hint:  hint,
					Badge: fluxBadge(f),
				})
			}
			return out, nil
		},
		PreviewHead: func(it tui.Item) string { return fluxHead(byKey[it.Key]) },
		PreviewBody: func(it tui.Item) string { return fluxEvents(cl, c.Name, byKey[it.Key], 8) },
	})
	if err != nil || it == nil {
		return err
	}
	f := byKey[it.Key]

	toggle := "suspend"
	if f.Suspended {
		toggle = "resume"
	}
	actions := []tui.Item{
		{Key: "reconcile", Label: fmt.Sprintf("%-10s", "reconcile"), Hint: "ask Flux to reconcile now and wait for the result"},
		{Key: "events", Label: fmt.Sprintf("%-10s", "events"), Hint: "kubectl events --for"},
		{Key: "describe", Label: fmt.Sprintf("%-10s", "describe"), Hint: "kubectl describe"},
		{Key: toggle, Label: fmt.Sprintf("%-10s", toggle), Hint: "patch spec.suspend"},
	}
	act, err := tui.Run(tui.Options{Title: fmt.Sprintf("%s %s/%s on %s", f.Kind, f.Namespace, f.Name, c.Short), Items: actions})
	if err != nil || act == nil {
		return err
	}

	switch act.Key {
	case "reconcile":
		return reconcile(cl, c, f)
	case "events":
		fmt.Println(fluxEvents(cl, c.Name, f, 0))
		return nil
	case "describe":
		return a.runKubectl(false, "--context", c.Name, "-n", f.Namespace, "describe", f.Resource(), f.Name)
	case "suspend", "resume":
		patch := fmt.Sprintf(`{"spec":{"suspend":%v}}`, act.Key == "suspend")
		// through the guard: suspending in production asks for confirmation
		return a.runKubectl(true, "--context", c.Name, "-n", f.Namespace, "patch", f.Resource(), f.Name, "--type", "merge", "-p", patch)
	}
	return nil
}

func fluxBadge(f kube.FluxItem) string {
	switch {
	case f.Failing():
		return "FAIL"
	case f.Suspended:
		return "SUSP"
	case f.Ready != "True":
		return "WAIT"
	}
	return "ok"
}

func fluxHead(f kube.FluxItem) string {
	row := func(k, v string) string { return tui.Dim.Render(fmt.Sprintf("%-10s", k)) + v }
	state := tui.Ok.Render("ready")
	switch {
	case f.Failing():
		state = tui.Err.Render("failing")
	case f.Suspended:
		state = tui.Warn.Render("suspended")
	case f.Ready != "True":
		state = tui.Warn.Render("reconciling")
	}
	lines := []string{
		tui.Bold.Render(f.Name) + "  " + state,
		"",
		row("kind", f.Kind),
		row("namespace", f.Namespace),
	}
	if f.SourceName != "" {
		lines = append(lines, row("source", f.SourceKind+" "+f.SourceNamespace+"/"+f.SourceName))
	}
	if f.AppliedRevision != "" {
		lines = append(lines, row("applied", f.AppliedRevision))
	}
	if f.AttemptedRevision != "" && f.AttemptedRevision != f.AppliedRevision {
		lines = append(lines, row("attempted", f.AttemptedRevision))
	}
	if f.Message != "" && f.Ready != "True" {
		lines = append(lines, "", tui.Muted.Render(truncate(oneLine(f.Message), 400)))
	}
	return strings.Join(lines, "\n")
}

// fluxEvents returns the item's recent events, oldest first (max lines; 0 = all).
func fluxEvents(cl kube.Client, ctxName string, f kube.FluxItem, max int) string {
	return objectEvents(cl, ctxName, f.Namespace, f.Kind, f.Name, max)
}

// objectEvents renders the last max events of one object (all when max <= 0).
func objectEvents(cl kube.Client, ctxName, ns, kind, name string, max int) string {
	out, err := cl.Get(ctxName, "get", "events", "-n", ns, "-o", "json",
		"--field-selector", "involvedObject.kind="+kind+",involvedObject.name="+name)
	if err != nil {
		return tui.Err.Render("events: " + err.Error())
	}
	evs, err := parseEvents(out)
	if err != nil {
		return tui.Err.Render("events: " + err.Error())
	}
	if len(evs) == 0 {
		return tui.Dim.Render("no recent events")
	}
	if max > 0 && len(evs) > max {
		evs = evs[len(evs)-max:]
	}
	lines := make([]string, 0, len(evs))
	for _, e := range evs {
		reason := tui.Ok.Render(e.Reason)
		if e.Type == "Warning" {
			reason = tui.Warn.Render(e.Reason)
		}
		count := ""
		if e.Count > 1 {
			count = tui.Dim.Render(fmt.Sprintf(" (x%d)", e.Count))
		}
		lines = append(lines, tui.Dim.Render(fmt.Sprintf("%-4s ", age(e.When)))+reason+count+" "+truncate(oneLine(e.Message), 300))
	}
	return strings.Join(lines, "\n")
}

type event struct {
	Type, Reason, Message string
	Count                 int
	When                  time.Time
}

func parseEvents(data []byte) ([]event, error) {
	var list struct {
		Items []struct {
			Type           string    `json:"type"`
			Reason         string    `json:"reason"`
			Message        string    `json:"message"`
			Count          int       `json:"count"`
			LastTimestamp  time.Time `json:"lastTimestamp"`
			EventTime      time.Time `json:"eventTime"`
			FirstTimestamp time.Time `json:"firstTimestamp"`
			Series         *struct {
				Count            int       `json:"count"`
				LastObservedTime time.Time `json:"lastObservedTime"`
			} `json:"series"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]event, 0, len(list.Items))
	for _, it := range list.Items {
		e := event{Type: it.Type, Reason: it.Reason, Message: it.Message, Count: it.Count}
		for _, t := range []time.Time{it.LastTimestamp, it.EventTime, it.FirstTimestamp} {
			if !t.IsZero() {
				e.When = t
				break
			}
		}
		if it.Series != nil {
			e.Count = it.Series.Count
			if !it.Series.LastObservedTime.IsZero() {
				e.When = it.Series.LastObservedTime
			}
		}
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].When.Before(out[j].When) })
	return out, nil
}

func reconcile(cl kube.Client, c kube.Context, f kube.FluxItem) error {
	token := time.Now().Format(time.RFC3339Nano)
	if _, err := cl.Get(c.Name, "annotate", "--overwrite", "-n", f.Namespace, f.Resource(), f.Name,
		"reconcile.fluxcd.io/requestedAt="+token); err != nil {
		return err
	}
	fmt.Printf("reconcile requested for %s %s/%s on %s ", f.Kind, f.Namespace, f.Name, c.Short)
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		fmt.Print(".")
		out, err := cl.Get(c.Name, "get", f.Resource(), f.Name, "-n", f.Namespace, "-o", "json")
		if err != nil {
			continue
		}
		items, err := kube.ParseFluxItems([]byte(`{"items":[` + string(out) + `]}`))
		if err != nil || len(items) == 0 {
			continue
		}
		cur := items[0]
		if cur.HandledReconcileAt != token || cur.Ready == "Unknown" {
			continue
		}
		fmt.Println()
		if cur.Ready == "True" {
			fmt.Println(tui.Ok.Render("ready") + tui.Dim.Render("  "+cur.AppliedRevision))
		} else {
			fmt.Println(tui.Err.Render("not ready: ") + oneLine(cur.Message))
		}
		return nil
	}
	fmt.Println()
	return errors.New("no result after 3 minutes (Flux may still be working on it)")
}

// runKubectl runs kubectl attached to the terminal. guarded=true goes through
// the production guard wrapper.
func (a *app) runKubectl(guarded bool, args ...string) error {
	bin := filepath.Join(a.binDir, "kubectl")
	if !guarded {
		var err error
		if bin, err = a.kubectl(); err != nil {
			return err
		}
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = setEnv(os.Environ(), "KUBECONFIG", a.src)
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		os.Exit(ee.ExitCode()) // kubectl/guard already printed why
	}
	return err
}
