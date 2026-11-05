package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kwhat answers "what changed?": a timeline, newest first, across every
// cluster — rollouts (new ReplicaSets / ControllerRevisions, with the image
// change) and what Flux did (applied changes, Helm upgrades, new commits).
//
//	kwhat [DURATION] [FILTER]   default the last hour; e.g. kwhat 6h api
//
// Flux's part comes from events, which the API server keeps about an hour.
func (a *app) kwhat(args []string) error {
	window, filter := time.Hour, ""
	for _, arg := range args {
		if d, err := time.ParseDuration(arg); err == nil && d > 0 {
			window = d
		} else {
			filter = arg
		}
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	cl.Timeout = 30 * time.Second
	raw, err := kube.Load(a.src)
	if err != nil {
		return err
	}
	clusters := uniqueClusters(kube.Contexts(raw, a.cfg))
	fmt.Fprintln(os.Stderr, tui.Dim.Render(fmt.Sprintf("reading changes of the last %s on %d clusters…", fmtWindow(window), len(clusters))))

	since := time.Now().Add(-window)
	var mu sync.Mutex
	var all []change
	var skipped []string
	var wg sync.WaitGroup
	for _, c := range clusters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cs, err := clusterChanges(cl, c, since)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				why := lastLogLine(err.Error())
				if kube.AuthExpired(err) {
					why = "login expired (kauth " + c.Short + ")"
				}
				skipped = append(skipped, c.Short+": skipped, "+why)
				return
			}
			all = append(all, cs...)
		}()
	}
	wg.Wait()
	fmt.Fprint(os.Stderr, "\033[1A\033[2K")

	if filter != "" {
		f := strings.ToLower(filter)
		kept := all[:0]
		for _, ch := range all {
			if strings.Contains(strings.ToLower(ch.what+" "+ch.detail), f) {
				kept = append(kept, ch)
			}
		}
		all = kept
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].when.After(all[j].when) })
	fmt.Print(renderChanges(all, window))
	sort.Strings(skipped)
	for _, s := range skipped {
		fmt.Fprintln(os.Stderr, tui.Warn.Render("  "+s))
	}
	return nil
}

type change struct {
	when    time.Time
	cluster kube.Context
	kind    string // rollout, flux, helm, git
	what    string // e.g. "deploy api/api"
	detail  string // e.g. "1.42.0 → 1.43.0"
	bad     bool
}

func clusterChanges(cl kube.Client, c kube.Context, since time.Time) ([]change, error) {
	var rsOut, crOut, evOut []byte
	var rsErr, crErr, evErr error
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); rsOut, rsErr = cl.Get(c.Name, "get", "replicasets", "-A", "-o", "json") }()
	go func() {
		defer wg.Done()
		crOut, crErr = cl.Get(c.Name, "get", "controllerrevisions", "-A", "-o", "json")
	}()
	go func() { defer wg.Done(); evOut, evErr = cl.Get(c.Name, "get", "events", "-A", "-o", "json") }()
	wg.Wait()
	if rsErr != nil {
		return nil, rsErr
	}
	var out []change
	out = append(out, rolloutChanges(rsOut, since)...)
	if crErr == nil {
		out = append(out, revisionChanges(crOut, since)...)
	}
	if evErr == nil {
		out = append(out, fluxChanges(evOut, since)...)
	}
	for i := range out {
		out[i].cluster = c
	}
	return out, nil
}

type ownedObj struct {
	Metadata struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		CreationTimestamp time.Time         `json:"creationTimestamp"`
		Annotations       map[string]string `json:"annotations"`
		OwnerReferences   []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Revision int64 `json:"revision"` // ControllerRevision
	Spec     struct {
		Template podTemplate `json:"template"` // ReplicaSet
	} `json:"spec"`
	Data struct { // ControllerRevision
		Spec struct {
			Template podTemplate `json:"template"`
		} `json:"spec"`
	} `json:"data"`
}

type podTemplate struct {
	Metadata struct {
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Containers []struct {
			Name  string `json:"name"`
			Image string `json:"image"`
		} `json:"containers"`
	} `json:"spec"`
}

func (o ownedObj) owner() (kind, name string) {
	for _, r := range o.Metadata.OwnerReferences {
		return r.Kind, r.Name
	}
	return "", ""
}

func templateImages(t podTemplate) map[string]string {
	m := map[string]string{}
	for _, c := range t.Spec.Containers {
		m[c.Name] = c.Image
	}
	return m
}

// imageChange describes what changed between two templates' images:
// "api 1.42.0 → 1.43.0", or "" when they are the same.
func imageChange(prev, cur map[string]string) string {
	var parts []string
	names := make([]string, 0, len(cur))
	for n := range cur {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if p, ok := prev[n]; ok && p != cur[n] {
			parts = append(parts, fmt.Sprintf("%s %s → %s", n, imageTag(p), imageTag(cur[n])))
		} else if !ok && prev != nil {
			parts = append(parts, fmt.Sprintf("+%s %s", n, imageTag(cur[n])))
		}
	}
	return strings.Join(parts, ", ")
}

// rolloutChanges: ReplicaSets created in the window are Deployment rollouts;
// the previous revision's ReplicaSet gives the image it replaced.
func rolloutChanges(data []byte, since time.Time) []change {
	var list struct{ Items []ownedObj }
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	type rev struct {
		n   int64
		obj ownedObj
	}
	byDeploy := map[string][]rev{}
	for _, o := range list.Items {
		kind, name := o.owner()
		if kind != "Deployment" {
			continue
		}
		n, _ := strconv.ParseInt(o.Metadata.Annotations["deployment.kubernetes.io/revision"], 10, 64)
		key := o.Metadata.Namespace + "/" + name
		byDeploy[key] = append(byDeploy[key], rev{n, o})
	}
	var out []change
	for key, revs := range byDeploy {
		sort.Slice(revs, func(i, j int) bool { return revs[i].n < revs[j].n })
		for i, r := range revs {
			if r.obj.Metadata.CreationTimestamp.Before(since) {
				continue
			}
			ch := change{when: r.obj.Metadata.CreationTimestamp, kind: "rollout", what: "deploy " + key}
			cur := templateImages(r.obj.Spec.Template)
			switch {
			case i == 0 && r.n <= 1:
				ch.detail = "created · " + imageList(cur)
			case i > 0:
				ch.detail = imageChange(templateImages(revs[i-1].obj.Spec.Template), cur)
			}
			if ch.detail == "" {
				ch.detail = "same image: config, env or restart"
				if _, ok := r.obj.Spec.Template.Metadata.Annotations["kubectl.kubernetes.io/restartedAt"]; ok && (i == 0 || revs[i-1].obj.Spec.Template.Metadata.Annotations["kubectl.kubernetes.io/restartedAt"] != r.obj.Spec.Template.Metadata.Annotations["kubectl.kubernetes.io/restartedAt"]) {
					ch.detail = "restart (rollout restart)"
				}
			}
			ch.detail = fmt.Sprintf("rev %d · %s", r.n, ch.detail)
			out = append(out, ch)
		}
	}
	return out
}

// revisionChanges: ControllerRevisions created in the window are StatefulSet
// and DaemonSet rollouts.
func revisionChanges(data []byte, since time.Time) []change {
	var list struct{ Items []ownedObj }
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	byOwner := map[string][]ownedObj{}
	kinds := map[string]string{}
	for _, o := range list.Items {
		kind, name := o.owner()
		if kind != "StatefulSet" && kind != "DaemonSet" {
			continue
		}
		key := o.Metadata.Namespace + "/" + name
		byOwner[key] = append(byOwner[key], o)
		kinds[key] = kind
	}
	var out []change
	for key, revs := range byOwner {
		sort.Slice(revs, func(i, j int) bool { return revs[i].Revision < revs[j].Revision })
		for i, r := range revs {
			if r.Metadata.CreationTimestamp.Before(since) || (i == 0 && r.Revision <= 1) {
				continue
			}
			detail := ""
			if i > 0 {
				detail = imageChange(templateImages(revs[i-1].Data.Spec.Template), templateImages(r.Data.Spec.Template))
			}
			if detail == "" {
				detail = "spec changed"
			}
			out = append(out, change{when: r.Metadata.CreationTimestamp, kind: "rollout",
				what: kube.ShortKind(kinds[key]) + " " + key, detail: fmt.Sprintf("rev %d · %s", r.Revision, detail)})
		}
	}
	return out
}

func imageList(m map[string]string) string {
	var parts []string
	for _, img := range m {
		parts = append(parts, imageTag(img))
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}

// fluxChanges: what the Flux controllers reported — applied changes, Helm
// installs/upgrades (and failures), new commits fetched.
func fluxChanges(data []byte, since time.Time) []change {
	var list struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Type           string `json:"type"`
			Reason         string `json:"reason"`
			Message        string `json:"message"`
			InvolvedObject struct {
				Kind      string `json:"kind"`
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"involvedObject"`
			LastTimestamp time.Time `json:"lastTimestamp"`
			EventTime     time.Time `json:"eventTime"`
		} `json:"items"`
	}
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	var out []change
	for _, e := range list.Items {
		when := e.LastTimestamp
		if when.IsZero() {
			when = e.EventTime
		}
		if when.Before(since) {
			continue
		}
		ns := e.InvolvedObject.Namespace
		if ns == "" {
			ns = e.Metadata.Namespace
		}
		obj := ns + "/" + e.InvolvedObject.Name
		msg := strings.TrimSpace(e.Message)
		ch := change{when: when, bad: e.Type == "Warning"}
		switch e.InvolvedObject.Kind {
		case "Kustomization":
			if e.Type != "Warning" && (e.Reason == "ReconciliationSucceeded" || !strings.Contains(msg, "/")) {
				continue // "reconciliation finished" with nothing applied
			}
			ch.kind, ch.what = "flux", "ks "+obj
			ch.detail = summarizeApplied(msg)
		case "HelmRelease":
			// installs/upgrades/rollbacks that succeeded, and every warning
			if e.Type != "Warning" && (!strings.Contains(e.Reason, "Succeeded") || e.Reason == "ReconciliationSucceeded") {
				continue
			}
			ch.kind, ch.what, ch.detail = "helm", "hr "+obj, truncate(oneLine(msg), 140)
		case "GitRepository", "OCIRepository", "HelmChart":
			if e.Reason != "NewArtifact" && e.Type != "Warning" {
				continue
			}
			ch.kind, ch.what, ch.detail = "git", strings.ToLower(e.InvolvedObject.Kind[:3])+" "+obj, truncate(oneLine(msg), 140)
		default:
			continue
		}
		out = append(out, ch)
	}
	return out
}

// summarizeApplied: kustomize-controller lists one "Kind/ns/name verb" per
// line; keep what was configured or created, compactly.
func summarizeApplied(msg string) string {
	var changed []string
	for _, l := range strings.Split(msg, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if strings.HasSuffix(l, " unchanged") {
			continue
		}
		changed = append(changed, l)
	}
	if len(changed) == 0 {
		return truncate(oneLine(msg), 140)
	}
	s := strings.Join(changed[:min(len(changed), 3)], ", ")
	if len(changed) > 3 {
		s += fmt.Sprintf(" … +%d", len(changed)-3)
	}
	return truncate(s, 160)
}

func renderChanges(cs []change, window time.Duration) string {
	if len(cs) == 0 {
		return tui.Ok.Render("nothing changed in the last "+fmtWindow(window)) + "\n"
	}
	cw, ww := 0, 0
	for _, c := range cs {
		cw = max(cw, len(c.cluster.Short))
		ww = max(ww, len(c.what))
	}
	width := 160
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 60 {
		width = w
	}
	room := max(width-(5+2+cw+2+7+1+ww+2), 30) // what's left for the detail
	var b strings.Builder
	day := ""
	for _, c := range cs {
		local := c.when.Local()
		if d := local.Format("Mon 02 Jan"); d != day && window > 20*time.Hour {
			day = d
			b.WriteString(tui.Bold.Render(d) + "\n")
		}
		cluster := fmt.Sprintf("%-*s", cw, c.cluster.Short)
		if c.cluster.Prod {
			cluster = tui.Err.Render(cluster)
		}
		kind := fmt.Sprintf("%-7s", c.kind)
		switch c.kind {
		case "rollout":
			kind = tui.Ok.Render(kind)
		case "flux", "helm":
			kind = tui.Warn.Render(kind)
		default:
			kind = tui.Dim.Render(kind)
		}
		detail := truncate(c.detail, room)
		if c.bad {
			detail = tui.Err.Render(detail)
		}
		fmt.Fprintf(&b, "%s  %s  %s %-*s  %s\n", tui.Dim.Render(local.Format("15:04")), cluster, kind, ww, c.what, detail)
	}
	fmt.Fprintf(&b, "\n%d change(s) in the last %s · production clusters in red\n", len(cs), fmtWindow(window))
	return b.String()
}

// fmtWindow: 24h0m0s → 24h, 1h30m0s → 1h30m, 30m0s → 30m.
func fmtWindow(d time.Duration) string {
	s := d.Round(time.Minute).String()
	s = strings.TrimSuffix(s, "0s")
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
