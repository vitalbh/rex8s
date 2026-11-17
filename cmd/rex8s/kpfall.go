package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// clusterService is a service and the cluster it lives on.
type clusterService struct {
	c kube.Context
	s service
}

func (cs clusterService) text() string {
	return cs.c.Short + " " + cs.s.Namespace + "/" + cs.s.Name
}

// matchWords reports whether every word of filter is in text, ignoring case:
// "grafana use" matches "use monitoring/grafana".
func matchWords(text, filter string) bool {
	t := strings.ToLower(text)
	for _, w := range strings.Fields(strings.ToLower(filter)) {
		if !strings.Contains(t, w) {
			return false
		}
	}
	return true
}

// kpfAll: kpf -A [FILTER] — the services of every cluster in one picker.
// When FILTER matches a single service, it starts that one without asking.
func (a *app) kpfAll(filter string) error {
	reads, skipped, err := a.readAllClusters("services", []string{"get", "services", "-A", "-o", "json"})
	if err != nil {
		return err
	}
	var all []clusterService
	for _, r := range reads {
		ss, err := parseServices(r.out[0])
		if err != nil {
			skipped = append(skipped, r.c.Short+": "+err.Error())
			continue
		}
		for _, s := range ss {
			all = append(all, clusterService{r.c, s})
		}
	}
	printSkipped(skipped)
	if len(all) == 0 {
		return fmt.Errorf("no TCP services on any cluster")
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].c.Short != all[j].c.Short {
			return all[i].c.Short < all[j].c.Short
		}
		if all[i].s.Namespace != all[j].s.Namespace {
			return all[i].s.Namespace < all[j].s.Namespace
		}
		return all[i].s.Name < all[j].s.Name
	})

	if filter != "" {
		var hit []clusterService
		for _, cs := range all {
			if matchWords(cs.text(), filter) {
				hit = append(hit, cs)
			}
		}
		if len(hit) > 1 { // an exact name wins over the ones that only contain it
			var exact []clusterService
			for _, cs := range hit {
				for _, w := range strings.Fields(strings.ToLower(filter)) {
					if w == strings.ToLower(cs.s.Name) || w == strings.ToLower(cs.s.Namespace+"/"+cs.s.Name) {
						exact = append(exact, cs)
						break
					}
				}
			}
			if len(exact) == 1 {
				hit = exact
			}
		}
		if len(hit) == 1 {
			return a.kpfStart(hit[0].c, hit[0].s)
		}
		if len(hit) == 0 {
			fmt.Fprintln(os.Stderr, tui.Dim.Render("no service matches "+strconv.Quote(filter)+"; showing all"))
			filter = ""
		}
	}

	wc, wn := 0, 0
	for _, cs := range all {
		wc = max(wc, len(cs.c.Short))
		wn = max(wn, len(cs.s.Namespace)+1+len(cs.s.Name))
	}
	items := make([]tui.Item, len(all))
	for i, cs := range all {
		items[i] = tui.Item{Key: strconv.Itoa(i),
			Label: fmt.Sprintf("%-*s  %-*s", wc, cs.c.Short, wn, cs.s.Namespace+"/"+cs.s.Name),
			Hint:  fmt.Sprintf("%-12s %s", cs.s.Type, cs.s.portsText()),
			Badge: prodBadgeBool(cs.c.Prod)}
	}
	it, err := tui.Run(tui.Options{Title: clustersTitle("Port-forward · services", len(reads), skipped), Items: items, Query: filter})
	if err != nil || it == nil {
		return err
	}
	i, _ := strconv.Atoi(it.Key)
	return a.kpfStart(all[i].c, all[i].s)
}
