package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

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
	cats := map[string]bool{}
	filter := ""
	for _, arg := range args {
		switch c := strings.TrimSuffix(strings.ToLower(arg), "s"); c {
		case "debug", "pod", "job", "pv":
			cats[c] = true
		default:
			filter = arg
		}
	}
	k, err := a.kubectl()
	if err != nil {
		return err
	}
	raw, err := kube.Load(a.src)
	if err != nil {
		return err
	}
	clusters := uniqueClusters(kube.Contexts(raw, a.cfg))
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	cl.Timeout = 40 * time.Second

	var found []junk
	var skipped []string
	load := func() ([]tui.Item, error) {
		found, skipped = nil, nil
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, c := range clusters {
			wg.Add(1)
			go func() {
				defer wg.Done()
				js, err := clusterJunk(cl, c, cats)
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
				found = append(found, js...)
			}()
		}
		wg.Wait()
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
	title := "Clean up · space marks, ctrl+a marks all shown"
	if len(cats) > 0 {
		var cs []string
		for c := range cats {
			cs = append(cs, c)
		}
		sort.Strings(cs)
		title += " · " + strings.Join(cs, ", ")
	}
	its, err := tui.RunMulti(tui.Options{Title: title, Load: load, Query: filter})
	for _, s := range skipped {
		fmt.Fprintln(os.Stderr, tui.Warn.Render("  "+s))
	}
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
	prodSet := map[string]bool{}
	var prod []string
	for _, j := range chosen {
		tag := ""
		if j.c.Prod {
			tag = " " + tui.Prod.Render(" PROD ")
			if !prodSet[j.c.Short] {
				prodSet[j.c.Short] = true
				prod = append(prod, j.c.Short)
			}
		}
		fmt.Printf("  %-8s %-5s %s%s\n", j.c.Short, j.cat, j.ref(), tag)
	}
	sort.Strings(prod)
	if len(prod) > 0 && os.Getenv("REX8S_FORCE") == "" && !confirmProd(fmt.Sprintf("Delete %d object(s) on", len(chosen)), prod) {
		fmt.Fprintln(os.Stderr, "cancelled.")
		return nil
	}
	failed := 0
	for _, j := range chosen {
		args := []string{"--context", j.c.Name, "delete", j.kind, j.name, "--wait=false"}
		if j.ns != "" {
			args = append(args, "-n", j.ns)
		}
		if j.kind == "job" {
			args = append(args, "--cascade=background") // its pods too
		}
		if _, err := kubectlOut(k, a.src, 30*time.Second, args...); err != nil {
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

func clusterJunk(cl kube.Client, c kube.Context, cats map[string]bool) ([]junk, error) {
	want := func(cat string) bool { return len(cats) == 0 || cats[cat] }
	var out []junk
	if want("debug") || want("pod") {
		data, err := cl.Get(c.Name, "get", "pods", "-A", "-o", "json")
		if err != nil {
			return nil, err
		}
		out = append(out, podJunk(data, time.Now(), want("debug"), want("pod"))...)
	}
	if want("job") {
		data, err := cl.Get(c.Name, "get", "jobs", "-A", "-o", "json")
		if err != nil {
			return nil, err
		}
		out = append(out, jobJunk(data, time.Now())...)
	}
	if want("pv") {
		if data, err := cl.Get(c.Name, "get", "pv", "-o", "json"); err == nil {
			out = append(out, pvJunk(data, time.Now())...)
		}
	}
	for i := range out {
		out[i].c = c
	}
	return out, nil
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
