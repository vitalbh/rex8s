package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/vitalbh/rex8s/internal/rex"
)

// Pods of the same workload share a key, so a crash loop whose pods get
// recreated doesn't notify again: "ctx|ns|workload|status".
var (
	// Kubernetes' generated suffixes use an alphabet without vowels
	// (bcdfghjklmnpqrstvwxz2456789), so a word like "exporter" isn't a hash.
	podHashRe = regexp.MustCompile(`-[bcdfghjklmnpqrstvwxz2456789]{6,10}-[bcdfghjklmnpqrstvwxz2456789]{5}$`) // Deployment
	podTailRe = regexp.MustCompile(`-([bcdfghjklmnpqrstvwxz2456789]{5}|\d+)$`)                               // DaemonSet/Job pod, StatefulSet ordinal
)

func podWorkload(name string) string {
	if w := podHashRe.ReplaceAllString(name, ""); w != name {
		return w
	}
	return podTailRe.ReplaceAllString(name, "")
}

func brokenKeys(cs []clusterFailingJSON) map[string]string {
	keys := map[string]string{} // key → "ns/workload status" for the message
	for _, c := range cs {
		for _, p := range c.Pods {
			w := podWorkload(p.Name)
			keys[strings.Join([]string{c.Context, p.Namespace, w, p.Status}, "|")] = fmt.Sprintf("%s/%s %s", p.Namespace, w, p.Status)
		}
	}
	return keys
}

// newBroken compares with what was seen last time; nil seen = first run
// (nothing is new: the current problems are only recorded).
func newBroken(seen map[string]bool, now map[string]string) []string {
	if seen == nil {
		return nil
	}
	var out []string
	for k := range now {
		if !seen[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// notifyNewBroken: one notification per cluster with pods that broke since
// the last check (config notifications: false turns it off).
func (a *app) notifyNewBroken(cs []clusterFailingJSON) {
	path := filepath.Join(a.cache, "failing-seen.json")
	var seen map[string]bool
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &seen)
		if seen == nil {
			seen = map[string]bool{}
		}
	}
	now := brokenKeys(cs)
	// a cluster we couldn't read keeps its previous state (no flood when it's back)
	unreadable := map[string]bool{}
	for _, c := range cs {
		if c.Error != "" {
			unreadable[c.Context] = true
		}
	}
	next := map[string]bool{}
	for k := range seen {
		if unreadable[strings.SplitN(k, "|", 2)[0]] {
			next[k] = true
		}
	}
	for k := range now {
		next[k] = true
	}
	if data, err := json.Marshal(next); err == nil {
		_ = os.MkdirAll(a.cache, 0o700)
		_ = os.WriteFile(path, data, 0o600)
	}

	byCluster := map[string][]string{}
	for _, k := range newBroken(seen, now) {
		ctx := strings.SplitN(k, "|", 2)[0]
		byCluster[ctx] = append(byCluster[ctx], now[k])
	}
	for _, c := range cs {
		msgs := byCluster[c.Context]
		if len(msgs) == 0 {
			continue
		}
		title := fmt.Sprintf("rex8s · %s: %d pod(s) broke", c.Short, len(msgs))
		if c.Prod {
			title = fmt.Sprintf("rex8s · %s (PROD): %d pod(s) broke", c.Short, len(msgs))
		}
		rex.Notify(title, strings.Join(msgs[:min(len(msgs), 3)], " · "))
	}
}
