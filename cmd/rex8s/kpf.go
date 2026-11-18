package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kpf: pick a service (and port) — every namespace, this pane's first; -n
// only this namespace; -A every cluster — and port-forward it in the
// background (rex8s _pf), printing the URL. kpf -l lists the running ones;
// kpf --stop PID stops one (for Raycast); kpf --gc cleans up after forwards
// that died.
func (a *app) kpf(args []string) error {
	filter, onlyNS := "", false
	if len(args) > 0 && args[0] == "-A" {
		return a.kpfAll(strings.Join(args[1:], " "))
	}
	if len(args) > 0 && args[0] == "-n" {
		onlyNS, args = true, args[1:]
	}
	if len(args) > 0 {
		switch args[0] {
		case "-l", "ls", "list":
			return a.kpfList()
		case "-f", "fav", "favorites":
			return a.kpfFavorites()
		case "--save": // kpf --save PID [NAME] (Raycast)
			if len(args) < 2 {
				return errors.New("kpf --save PID [NAME]")
			}
			pid, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("bad pid %q", args[1])
			}
			name := ""
			if len(args) > 2 {
				name = args[2]
			}
			for _, e := range a.pfList() {
				if e.PID == pid {
					saved, err := a.saveFavorite(e, name)
					if err != nil {
						return err
					}
					fmt.Println(saved)
					return nil
				}
			}
			return fmt.Errorf("no port-forward with pid %d", pid)
		case "--gc":
			for _, f := range a.registry().GC() {
				fmt.Println("removed " + f)
			}
			return nil
		case "--stop":
			if len(args) < 2 {
				return errors.New("kpf --stop PID")
			}
			pid, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("bad pid %q", args[1])
			}
			return a.registry().Stop(pid)
		}
		if f, ok := a.favorite(args[0]); ok {
			return a.fwd(f.Name)
		}
		filter = args[0]
	}
	c, err := kube.Current(a.cfg, "", "")
	if err != nil {
		return err
	}
	here := nsOr(c.Namespace)
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	scope, where := []string{"-A"}, c.Short+" (all namespaces, "+here+" first)"
	if onlyNS {
		scope, where = []string{"-n", here}, c.Short+"/"+here
	}

	svcs := map[string]kube.Service{}
	it, err := tui.Run(tui.Options{
		Title: "Port-forward · services in " + where,
		Query: filter,
		Load: func() ([]tui.Item, error) {
			out, err := cl.Get(c.Name, append([]string{"get", "services", "-o", "json"}, scope...)...)
			if err != nil {
				return nil, err
			}
			ss, err := kube.ParseServices(out)
			if err != nil {
				return nil, err
			}
			if len(ss) == 0 {
				return nil, fmt.Errorf("no TCP services in %s", where)
			}
			// this pane's namespace first, then by namespace and name
			sort.SliceStable(ss, func(i, j int) bool {
				if hi, hj := ss[i].Namespace == here, ss[j].Namespace == here; hi != hj {
					return hi
				}
				if ss[i].Namespace != ss[j].Namespace {
					return ss[i].Namespace < ss[j].Namespace
				}
				return ss[i].Name < ss[j].Name
			})
			w := 0
			for _, s := range ss {
				w = max(w, len(s.Namespace)+1+len(s.Name))
			}
			items := make([]tui.Item, 0, len(ss))
			for _, s := range ss {
				key := s.Namespace + "/" + s.Name
				svcs[key] = s
				items = append(items, tui.Item{Key: key, Label: fmt.Sprintf("%-*s", w, key),
					Hint: fmt.Sprintf("%-12s %s", s.Type, s.PortsText())})
			}
			return items, nil
		},
	})
	if err != nil || it == nil {
		return err
	}
	return a.kpfStart(c, svcs[it.Key])
}

// kpfStart asks for the port when the service has several, then starts the
// forward in the background on a free local port.
func (a *app) kpfStart(c kube.Context, s kube.Service) error {
	ns := s.Namespace
	port := s.Ports[0]
	if len(s.Ports) > 1 {
		opts := make([]tui.Item, len(s.Ports))
		for i, p := range s.Ports {
			opts[i] = tui.Item{Key: strconv.Itoa(i), Label: fmt.Sprintf("%-6d", p.Port), Hint: strings.TrimSpace(p.Name + "  " + string(detectKind(p)))}
		}
		ch, err := tui.Run(tui.Options{Title: "Port of " + s.Name, Items: opts})
		if err != nil || ch == nil {
			return err
		}
		i, _ := strconv.Atoi(ch.Key)
		port = s.Ports[i]
	}

	return a.lazyStart(c, ns, s.Name, port)
}
