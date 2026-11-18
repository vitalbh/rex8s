package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

func serviceRef(s kube.Service) (string, string) { return s.Namespace, s.Name }

// serviceText is what kpf -A matches a filter against: "use monitoring/grafana".
func serviceText(cs kube.On[kube.Service]) string {
	return cs.C.Short + " " + cs.V.Namespace + "/" + cs.V.Name
}

// kpfAll: kpf -A [FILTER] — the services of every cluster in one picker.
// When FILTER matches a single service, it starts that one without asking.
func (a *app) kpfAll(filter string) error {
	res, err := readAll(a, "services", [][]string{{"get", "services", "-A", "-o", "json"}},
		func(_ kube.Context, out [][]byte) ([]kube.Service, error) { return kube.ParseServices(out[0]) })
	if err != nil {
		return err
	}
	all := res.Rows
	printSkipped(res.Skipped)
	if len(all) == 0 {
		return fmt.Errorf("no TCP services on any cluster")
	}
	fleet.Sort(all, serviceRef)

	if filter != "" {
		var hit []kube.On[kube.Service]
		for _, cs := range all {
			if matchWords(serviceText(cs), filter) {
				hit = append(hit, cs)
			}
		}
		if len(hit) > 1 { // an exact name wins over the ones that only contain it
			var exact []kube.On[kube.Service]
			for _, cs := range hit {
				for _, w := range strings.Fields(strings.ToLower(filter)) {
					if w == strings.ToLower(cs.V.Name) || w == strings.ToLower(cs.V.Namespace+"/"+cs.V.Name) {
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
			return a.kpfStart(hit[0].C, hit[0].V)
		}
		if len(hit) == 0 {
			fmt.Fprintln(os.Stderr, tui.Dim.Render("no service matches "+strconv.Quote(filter)+"; showing all"))
			filter = ""
		}
	}

	wc, wn := 0, 0
	for _, cs := range all {
		wc = max(wc, len(cs.C.Short))
		wn = max(wn, len(cs.V.Namespace)+1+len(cs.V.Name))
	}
	items := make([]tui.Item, len(all))
	for i, cs := range all {
		items[i] = tui.Item{Key: strconv.Itoa(i),
			Label: fmt.Sprintf("%-*s  %-*s", wc, cs.C.Short, wn, cs.V.Namespace+"/"+cs.V.Name),
			Hint:  fmt.Sprintf("%-12s %s", cs.V.Type, cs.V.PortsText()),
			Badge: prodBadgeBool(cs.C.Prod)}
	}
	it, err := tui.Run(tui.Options{Title: clustersTitle("Port-forward · services", res.OK, len(res.Skipped)), Items: items, Query: filter})
	if err != nil || it == nil {
		return err
	}
	i, _ := strconv.Atoi(it.Key)
	return a.kpfStart(all[i].C, all[i].V)
}
