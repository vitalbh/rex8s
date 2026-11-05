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
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kincident opens a Rex session for an incident on one cluster, already
// laid out: kstatus -w and the live warnings on the left; on the right
// what changed lately (kwhat) and a shell on the cluster.
//
//	kincident [CLUSTER]       this pane's cluster when none is given
//	kincident --report [ID]   a Markdown report of the incident so far
//	                          (timeline, warnings, broken pods) for the postmortem
//	kincident --ask [ID]      the same report, then a kask conversation on it
func (a *app) kincident(args []string) error {
	mode, name := "open", ""
	for _, arg := range args {
		switch arg {
		case "--report":
			mode = "report"
		case "--ask":
			mode = "ask"
		default:
			name = arg
		}
	}
	if mode != "open" {
		return a.incidentReport(name, mode == "ask")
	}
	var c kube.Context
	var err error
	if name != "" {
		c, err = a.clusterContext(name)
	} else {
		c, err = kube.Current(a.cfg, "", "")
	}
	if err != nil {
		return err
	}
	started := time.Now()
	label := fmt.Sprintf("incident-%s-%s", c.Short, started.Format("0102-1504"))

	out, err := exec.Command(rex.Bin(), "new", label, "--json").Output()
	if err != nil {
		return fmt.Errorf("rex new: %v (is Rex running?)", err)
	}
	var created struct {
		SessionID      string `json:"session_id"`
		InitialWindows []struct {
			WindowID string `json:"window_id"`
		} `json:"initial_windows"`
	}
	if err := json.Unmarshal(out, &created); err != nil || created.SessionID == "" {
		return fmt.Errorf("rex new: unexpected output %q", strings.TrimSpace(string(out)))
	}
	sid := created.SessionID
	argsJSON, _ := json.Marshal(map[string]string{"ctx": c.Name})
	if out, err := exec.Command(rex.Bin(), "-s", sid, "do", "k8s_incident", "--args", string(argsJSON)).CombinedOutput(); err != nil {
		return fmt.Errorf("k8s_incident: %v: %s (is rex8s.lua up to date? cp rex/rex8s.lua ~/.config/rex/ && rex config reload)", err, strings.TrimSpace(string(out)))
	}
	// the plain shell rex new started is not needed
	for _, w := range created.InitialWindows {
		body, _ := json.Marshal(map[string]string{"window_id": w.WindowID})
		_ = exec.Command(rex.Bin(), "api", "call", "-s", sid, "session.close_window", string(body)).Run()
	}
	_ = a.saveIncident(incident{ID: label, Context: c.Name, Short: c.Short, Prod: c.Prod, Session: sid, Started: started})
	// show it (from a pane this needs Remote Control; harmless when off)
	_ = exec.Command(rex.Bin(), "do", "session.select", "session_id="+sid).Run()
	fmt.Printf("%s %s\n", tui.Bold.Render("incident session"), label)
	fmt.Println(tui.Dim.Render("kincident --report: a Markdown summary for the postmortem · kincident --ask: the same, then talk to Claude about it"))
	return nil
}

type incident struct {
	ID      string    `json:"id"`
	Context string    `json:"context"`
	Short   string    `json:"short"`
	Prod    bool      `json:"prod"`
	Session string    `json:"session"`
	Started time.Time `json:"started"`
}

func (a *app) incidentDir() string { return filepath.Join(a.cache, "incidents") }

func (a *app) saveIncident(in incident) error {
	if err := os.MkdirAll(a.incidentDir(), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(in, "", "  ")
	return os.WriteFile(filepath.Join(a.incidentDir(), in.ID+".json"), data, 0o600)
}

// findIncident: by ID (or a part of it), else the one of this Rex session,
// else the latest.
func (a *app) findIncident(id string) (incident, error) {
	files, _ := filepath.Glob(filepath.Join(a.incidentDir(), "*.json"))
	var all []incident
	for _, f := range files {
		var in incident
		if data, err := os.ReadFile(f); err == nil && json.Unmarshal(data, &in) == nil {
			all = append(all, in)
		}
	}
	if len(all) == 0 {
		return incident{}, errors.New("no incident yet: start one with kincident [cluster]")
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Started.After(all[j].Started) })
	for _, in := range all {
		if id != "" && strings.Contains(in.ID, id) {
			return in, nil
		}
		if id == "" && in.Session != "" && in.Session == rex.Session() {
			return in, nil
		}
	}
	if id != "" {
		return incident{}, fmt.Errorf("no incident matching %q", id)
	}
	return all[0], nil
}

func (a *app) incidentReport(id string, ask bool) error {
	in, err := a.findIncident(id)
	if err != nil {
		return err
	}
	c, err := a.context(in.Context)
	if err != nil {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	cl.Timeout = 30 * time.Second
	fmt.Fprint(os.Stderr, tui.Dim.Render("gathering the incident's timeline… "))
	since := in.Started.Add(-time.Hour) // what led to it, too
	changes, err := clusterChanges(cl, c, since)
	if err != nil {
		return err
	}
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].when.Before(changes[j].when) })

	var b strings.Builder
	prod := ""
	if c.Prod {
		prod = " (production)"
	}
	fmt.Fprintf(&b, "Incident %s on cluster %s%s.\nStarted (session opened): %s · report made %s · %s so far.\n\n",
		in.ID, c.Short, prod, in.Started.Format("2006-01-02 15:04"), time.Now().Format("15:04"), time.Since(in.Started).Round(time.Minute))
	b.WriteString("## Timeline: changes from an hour before the start until now\n```\n")
	if len(changes) == 0 {
		b.WriteString("no rollouts or Flux changes in that window\n")
	}
	for _, ch := range changes {
		mark := ""
		if ch.bad {
			mark = " [failed]"
		}
		fmt.Fprintf(&b, "%s  %-7s %s  %s%s\n", ch.when.Local().Format("15:04"), ch.kind, ch.what, ch.detail, mark)
	}
	b.WriteString("```\n\n")
	b.WriteString(clusterSnapshot(cl, c))

	file, err := a.writeDump(c, "incident "+in.ID, b.String())
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, tui.Dim.Render(tildePath(file)))
	if !ask {
		fmt.Println(file)
		return nil
	}
	if rex.Block() == "" {
		return a.asktail(c.Name, nsOr(c.Namespace), file)
	}
	return rex.Split("ask "+in.ID, filepath.Join(a.binDir, "rex8s"), "asktail", c.Name, nsOr(c.Namespace), file)
}
