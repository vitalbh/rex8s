package main

import (
	"encoding/json"
	"errors"
	"os"
	"sort"

	"github.com/vitalbh/rex8s/internal/kube"
)

// list prints JSON for launchers (the Raycast extension):
//
//	rex8s list clusters
//	rex8s list namespaces CTX
//	rex8s list pods CTX [NS]       all namespaces when NS is empty
//	rex8s list services CTX [NS]
//	rex8s list flux                Flux health of every cluster (menu bar)
//	rex8s list flux CTX            Flux resources of one cluster
//
// Errors are JSON too: {"error": "...", "loginExpired": true}.
func (a *app) list(args []string) error {
	out, err := a.listData(args)
	enc := json.NewEncoder(os.Stdout)
	if err != nil {
		short := ""
		if len(args) > 1 {
			if c, e := a.context(args[1]); e == nil {
				short = c.Short
			}
		}
		_ = enc.Encode(map[string]any{"error": err.Error(), "loginExpired": kube.AuthExpired(err), "cluster": short})
		os.Exit(1)
	}
	return enc.Encode(out)
}

type clusterJSON struct {
	Context   string `json:"context"`
	Short     string `json:"short"`
	Prod      bool   `json:"prod"`
	Server    string `json:"server"`
	Namespace string `json:"namespace"`
	User      string `json:"user"`
}

type podJSON struct {
	Namespace  string   `json:"namespace"`
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Badge      string   `json:"badge"`
	Ready      int      `json:"ready"`
	Total      int      `json:"total"`
	Restarts   int      `json:"restarts"`
	Age        string   `json:"age"`
	Containers []string `json:"containers"`
}

type portJSON struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

type serviceJSON struct {
	Namespace string     `json:"namespace"`
	Name      string     `json:"name"`
	Type      string     `json:"type"`
	Ports     []portJSON `json:"ports"`
}

func (a *app) listData(args []string) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("usage: rex8s list clusters|namespaces|pods|services [CTX] [NS]")
	}
	if args[0] == "flux" && len(args) == 1 {
		return a.fluxHealthAll()
	}
	if args[0] == "clusters" {
		raw, err := kube.Load(a.src)
		if err != nil {
			return nil, err
		}
		out := []clusterJSON{}
		for _, c := range kube.Contexts(raw, a.cfg) {
			out = append(out, clusterJSON{Context: c.Name, Short: c.Short, Prod: c.Prod, Server: c.Server, Namespace: c.Namespace, User: c.User})
		}
		return out, nil
	}
	if len(args) < 2 {
		return nil, errors.New("missing context")
	}
	ctxName, ns := args[1], ""
	if len(args) > 2 {
		ns = args[2]
	}
	if _, err := a.context(ctxName); err != nil {
		return nil, err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return nil, err
	}
	scope := []string{"-A"}
	if ns != "" {
		scope = []string{"-n", ns}
	}

	switch args[0] {
	case "flux":
		items, installed, err := cl.FluxItems(ctxName)
		if err != nil {
			return nil, err
		}
		out := []fluxJSON{}
		if installed {
			for _, it := range items {
				out = append(out, toFluxJSON(it))
			}
		}
		return out, nil
	case "namespaces":
		return cl.Namespaces(ctxName)
	case "pods":
		data, err := cl.Get(ctxName, append([]string{"get", "pods", "-o", "json"}, scope...)...)
		if err != nil {
			return nil, err
		}
		ps, err := parsePods(data)
		if err != nil {
			return nil, err
		}
		rank := map[string]int{"FAIL": 0, "WAIT": 1, "ok": 2, "done": 3}
		sort.SliceStable(ps, func(i, j int) bool { return rank[ps[i].badge()] < rank[ps[j].badge()] })
		out := make([]podJSON, 0, len(ps))
		for _, p := range ps {
			out = append(out, podJSON{Namespace: p.Namespace, Name: p.Name, Status: p.status(), Badge: p.badge(),
				Ready: p.Ready, Total: p.Total, Restarts: p.Restarts, Age: age(p.Created), Containers: p.Containers})
		}
		return out, nil
	case "services":
		data, err := cl.Get(ctxName, append([]string{"get", "services", "-o", "json"}, scope...)...)
		if err != nil {
			return nil, err
		}
		ss, err := parseServices(data)
		if err != nil {
			return nil, err
		}
		out := make([]serviceJSON, 0, len(ss))
		for _, s := range ss {
			sj := serviceJSON{Namespace: s.Namespace, Name: s.Name, Type: s.Type}
			for _, p := range s.Ports {
				sj.Ports = append(sj.Ports, portJSON{Name: p.Name, Port: p.Port})
			}
			out = append(out, sj)
		}
		return out, nil
	}
	return nil, errors.New("unknown list: " + args[0])
}

type fluxJSON struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	State     string `json:"state"` // ready, failing, suspended, reconciling
	Message   string `json:"message"`
	Revision  string `json:"revision"`
	Source    string `json:"source"`
}

func toFluxJSON(it kube.FluxItem) fluxJSON {
	f := fluxJSON{Kind: it.Kind, Namespace: it.Namespace, Name: it.Name, Message: oneLine(it.Message), Revision: it.AppliedRevision}
	switch {
	case it.Failing():
		f.State = "failing"
	case it.Suspended:
		f.State = "suspended"
	case it.Ready != "True":
		f.State = "reconciling"
	default:
		f.State = "ready"
	}
	if it.SourceName != "" {
		f.Source = it.SourceKind + " " + it.SourceNamespace + "/" + it.SourceName
	}
	return f
}

type clusterHealthJSON struct {
	Context      string     `json:"context"`
	Short        string     `json:"short"`
	Prod         bool       `json:"prod"`
	Error        string     `json:"error,omitempty"`
	LoginExpired bool       `json:"loginExpired,omitempty"`
	Installed    bool       `json:"installed"`
	Ready        int        `json:"ready"`
	Failing      int        `json:"failing"`
	Reconciling  int        `json:"reconciling"`
	Suspended    int        `json:"suspended"`
	Problems     []fluxJSON `json:"problems"`
}

// fluxHealthAll polls every cluster (one per server) in parallel.
func (a *app) fluxHealthAll() ([]clusterHealthJSON, error) {
	raw, err := kube.Load(a.src)
	if err != nil {
		return nil, err
	}
	clusters := dedupe(kube.Contexts(raw, a.cfg))
	cl, err := a.client(a.src)
	if err != nil {
		return nil, err
	}
	health := pollAll(cl, clusters)
	out := make([]clusterHealthJSON, 0, len(clusters))
	for _, c := range clusters {
		h := health[c.Name]
		j := clusterHealthJSON{Context: c.Name, Short: c.Short, Prod: c.Prod, Installed: h.installed, Problems: []fluxJSON{}}
		if h.err != "" {
			j.Error, j.LoginExpired = h.err, kube.AuthExpired(errors.New(h.err))
		}
		st := kube.Summarize(h.items)
		j.Ready, j.Failing, j.Reconciling, j.Suspended = st.Ready, st.Failing, st.Progressing, st.Suspended
		for _, it := range h.items {
			if it.Failing() {
				j.Problems = append(j.Problems, toFluxJSON(it))
			}
		}
		out = append(out, j)
	}
	return out, nil
}

// freconcile asks Flux to reconcile one resource and waits for the result,
// printing one line (for launchers): rex8s freconcile CTX KIND NS NAME
func (a *app) freconcile(args []string) error {
	if len(args) < 4 {
		return errors.New("usage: rex8s freconcile CTX KIND NS NAME")
	}
	c, err := a.context(args[0])
	if err != nil {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	return reconcile(cl, c, kube.FluxItem{Kind: args[1], Namespace: args[2], Name: args[3]})
}
