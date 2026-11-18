package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kjunk: leftovers on every cluster that can go — mark them and delete.
//
//	kjunk [debug|pods|jobs|pv] [FILTER]
//
//	debug  node-debugger-* (knode, kubectl debug) and knet-* pods that ended,
//	       or run for over a day
//	pods   Failed pods (Evicted, errors) older than a day, not owned by a Job
//	jobs   finished Jobs older than a week, not owned by a CronJob (those
//	       follow its history limits)
//	pv     Released PersistentVolumes (Retain: deleting the PV object keeps
//	       the data on the storage)
//
// Production asks once (y/N) before deleting.
func (a *app) kjunk(args []string) error {
	cats, filter := parseKjunkArgs(args)
	raw, err := kube.Load(a.src)
	if err != nil {
		return err
	}
	clusters := fleet.All(raw, a.cfg)
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	cl.Timeout = 40 * time.Second

	var found []junk
	var skipped []fleet.Skip
	r := a.readerFor(cl)
	queries := junkQueries(cats)
	load := func() ([]tui.Item, error) {
		res := fleet.Read(r, clusters, queries, func(c kube.Context, out [][]byte) ([]junk, error) {
			return clusterJunk(cl, c, cats, out), nil
		})
		found, skipped = nil, res.Skipped
		for _, row := range res.Rows {
			found = append(found, row.V)
		}
		if len(found) == 0 {
			return nil, fmt.Errorf("nothing to clean up on %d clusters", len(clusters))
		}
		sort.SliceStable(found, func(i, j int) bool {
			if found[i].cat != found[j].cat {
				return found[i].cat < found[j].cat
			}
			if found[i].c.Short != found[j].c.Short {
				return found[i].c.Short < found[j].c.Short
			}
			return found[i].age > found[j].age
		})
		cw, nw := 0, 0
		for _, j := range found {
			cw = max(cw, len(j.c.Short))
			nw = max(nw, len(j.ns)+1+len(j.name))
		}
		nw = min(nw, 64)
		items := make([]tui.Item, 0, len(found))
		for i, j := range found {
			items = append(items, tui.Item{Key: fmt.Sprint(i), Badge: prodBadgeBool(j.c.Prod),
				Label: fmt.Sprintf("%-*s  %-5s %-*s", cw, j.c.Short, j.cat, nw, clip(j.ref(), nw)),
				Hint:  fmt.Sprintf("%-6s %s", fmtAge(j.age), j.why)})
		}
		return items, nil
	}
	its, err := tui.RunMulti(tui.Options{Title: kjunkTitle(cats), Load: load, Query: filter})
	printSkipped(skipped)
	if err != nil || len(its) == 0 {
		return err
	}
	var chosen []junk
	for _, it := range its {
		var i int
		fmt.Sscan(it.Key, &i)
		chosen = append(chosen, found[i])
	}

	fmt.Println(tui.Bold.Render(fmt.Sprintf("delete %d object(s)", len(chosen))))
	touched := make([]kube.Context, 0, len(chosen))
	for _, j := range chosen {
		tag := ""
		if j.c.Prod {
			tag = " " + tui.Prod.Render(" PROD ")
		}
		touched = append(touched, j.c)
		fmt.Printf("  %-8s %-5s %s%s\n", j.c.Short, j.cat, j.ref(), tag)
	}
	if ok, err := confirmChange(touched, fmt.Sprintf("kjunk: delete %d object(s)", len(chosen))); !ok {
		return err
	}
	return deleteJunk(cl, chosen)
}

// parseKjunkArgs: kjunk [debug|pods|jobs|pv]... [FILTER].
func parseKjunkArgs(args []string) (cats map[string]bool, filter string) {
	cats = map[string]bool{}
	for _, arg := range args {
		switch c := strings.TrimSuffix(strings.ToLower(arg), "s"); c {
		case "debug", "pod", "job", "pv":
			cats[c] = true
		default:
			filter = arg
		}
	}
	return cats, filter
}

func kjunkTitle(cats map[string]bool) string {
	title := "Clean up · space marks, ctrl+a marks all shown"
	if len(cats) > 0 {
		var cs []string
		for c := range cats {
			cs = append(cs, c)
		}
		sort.Strings(cs)
		title += " · " + strings.Join(cs, ", ")
	}
	return title
}

// deleteJunk deletes the chosen objects one by one, printing each result.
func deleteJunk(cl kube.Client, chosen []junk) error {
	failed := 0
	for _, j := range chosen {
		args := []string{"delete", j.kind, j.name, "--wait=false"}
		if j.ns != "" {
			args = append(args, "-n", j.ns)
		}
		if j.kind == "job" {
			args = append(args, "--cascade=background") // its pods too
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := cl.Output(ctx, j.c.Name, args...)
		cancel()
		if err != nil {
			failed++
			fmt.Println(tui.Err.Render("  ✗ "+j.c.Short+" "+j.ref()) + "  " + tui.Dim.Render(lastLogLine(err.Error())))
			continue
		}
		fmt.Println(tui.Ok.Render("  ✓ ") + j.c.Short + " " + j.ref())
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d not deleted", failed, len(chosen))
	}
	return nil
}

type junk struct {
	c        kube.Context
	cat      string // debug, pod, job, pv
	kind     string // for kubectl delete
	ns, name string
	age      time.Duration
	why      string
}

func (j junk) ref() string {
	if j.ns == "" {
		return j.name
	}
	return j.ns + "/" + j.name
}

func fmtAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

const (
	junkPodAge = 24 * time.Hour
	junkJobAge = 7 * 24 * time.Hour
)

// junkQueries: what kjunk reads from each cluster for cats (all when
// empty): pods for debug and pod, jobs for job. PVs are read on their own
// (clusterJunk), since many users may not list them.
func junkQueries(cats map[string]bool) [][]string {
	want := func(cat string) bool { return len(cats) == 0 || cats[cat] }
	var q [][]string
	if want("debug") || want("pod") {
		q = append(q, []string{"get", "pods", "-A", "-o", "json"})
	}
	if want("job") {
		q = append(q, []string{"get", "jobs", "-A", "-o", "json"})
	}
	return q
}

// clusterJunk finds c's leftovers in out, the answers to junkQueries(cats),
// and reads its Released PVs when cats wants them.
func clusterJunk(cl kube.Client, c kube.Context, cats map[string]bool, out [][]byte) []junk {
	want := func(cat string) bool { return len(cats) == 0 || cats[cat] }
	var js []junk
	if want("debug") || want("pod") {
		js = append(js, podJunk(out[0], time.Now(), want("debug"), want("pod"))...)
		out = out[1:]
	}
	if want("job") {
		js = append(js, jobJunk(out[0], time.Now())...)
	}
	if want("pv") {
		if data, err := cl.Get(c.Name, "get", "pv", "-o", "json"); err == nil {
			js = append(js, pvJunk(data, time.Now())...)
		}
	}
	for i := range js {
		js[i].c = c
	}
	return js
}

type ownerMeta struct {
	Name              string    `json:"name"`
	Namespace         string    `json:"namespace"`
	CreationTimestamp time.Time `json:"creationTimestamp"`
	OwnerReferences   []struct {
		Kind string `json:"kind"`
	} `json:"ownerReferences"`
}

func (m ownerMeta) ownedBy(kind string) bool {
	for _, o := range m.OwnerReferences {
		if o.Kind == kind {
			return true
		}
	}
	return false
}

func podJunk(data []byte, now time.Time, debug, failed bool) []junk {
	var list struct {
		Items []struct {
			Metadata ownerMeta `json:"metadata"`
			Status   struct {
				Phase  string `json:"phase"`
				Reason string `json:"reason"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	var out []junk
	for _, p := range list.Items {
		age := now.Sub(p.Metadata.CreationTimestamp)
		ended := p.Status.Phase == "Succeeded" || p.Status.Phase == "Failed"
		isDebug := strings.HasPrefix(p.Metadata.Name, "node-debugger-") || strings.HasPrefix(p.Metadata.Name, "knet-")
		switch {
		case isDebug:
			if debug && (ended || age > 24*time.Hour) {
				why := "debug pod, " + strings.ToLower(firstNonEmpty(p.Status.Phase, "unknown"))
				out = append(out, junk{cat: "debug", kind: "pod", ns: p.Metadata.Namespace, name: p.Metadata.Name, age: age, why: why})
			}
		case failed && p.Status.Phase == "Failed" && age > junkPodAge && !p.Metadata.ownedBy("Job"):
			out = append(out, junk{cat: "pod", kind: "pod", ns: p.Metadata.Namespace, name: p.Metadata.Name, age: age,
				why: "Failed " + p.Status.Reason})
		}
	}
	return out
}

func jobJunk(data []byte, now time.Time) []junk {
	var list struct {
		Items []struct {
			Metadata ownerMeta `json:"metadata"`
			Spec     struct {
				TTL *int `json:"ttlSecondsAfterFinished"`
			} `json:"spec"`
			Status struct {
				CompletionTime *time.Time `json:"completionTime"`
				Conditions     []struct {
					Type               string    `json:"type"`
					Status             string    `json:"status"`
					LastTransitionTime time.Time `json:"lastTransitionTime"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	var out []junk
	for _, j := range list.Items {
		if j.Metadata.ownedBy("CronJob") || j.Spec.TTL != nil {
			continue // the CronJob's history limit or the TTL cleans these
		}
		var done time.Time
		state := ""
		for _, c := range j.Status.Conditions {
			if (c.Type == "Complete" || c.Type == "Failed") && c.Status == "True" {
				done, state = c.LastTransitionTime, strings.ToLower(c.Type)
			}
		}
		if j.Status.CompletionTime != nil {
			done = *j.Status.CompletionTime
		}
		if done.IsZero() || now.Sub(done) < junkJobAge {
			continue
		}
		out = append(out, junk{cat: "job", kind: "job", ns: j.Metadata.Namespace, name: j.Metadata.Name,
			age: now.Sub(done), why: "finished (" + state + ") " + fmtAge(now.Sub(done)) + " ago"})
	}
	return out
}

func pvJunk(data []byte, now time.Time) []junk {
	var list struct {
		Items []struct {
			Metadata ownerMeta `json:"metadata"`
			Spec     struct {
				ClaimRef *struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"claimRef"`
				Reclaim  string            `json:"persistentVolumeReclaimPolicy"`
				Capacity map[string]string `json:"capacity"`
			} `json:"spec"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if json.Unmarshal(data, &list) != nil {
		return nil
	}
	var out []junk
	for _, pv := range list.Items {
		if pv.Status.Phase != "Released" {
			continue
		}
		why := fmt.Sprintf("Released %s", pv.Spec.Capacity["storage"])
		if pv.Spec.ClaimRef != nil {
			why += " · was " + pv.Spec.ClaimRef.Namespace + "/" + pv.Spec.ClaimRef.Name
		}
		if pv.Spec.Reclaim == "Retain" {
			why += " · Retain: the data stays on the storage"
		}
		out = append(out, junk{cat: "pv", kind: "pv", name: pv.Metadata.Name, age: now.Sub(pv.Metadata.CreationTimestamp), why: why})
	}
	return out
}
