package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

type svcPort struct {
	Name     string
	Port     int
	Protocol string
}

type service struct {
	Namespace string
	Name      string
	Type      string
	Ports     []svcPort
}

func parseServices(data []byte) ([]service, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Type  string `json:"type"`
				Ports []struct {
					Name     string `json:"name"`
					Port     int    `json:"port"`
					Protocol string `json:"protocol"`
				} `json:"ports"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]service, 0, len(list.Items))
	for _, it := range list.Items {
		s := service{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, Type: it.Spec.Type}
		for _, p := range it.Spec.Ports {
			if p.Protocol == "" || p.Protocol == "TCP" { // port-forward is TCP only
				s.Ports = append(s.Ports, svcPort{Name: p.Name, Port: p.Port, Protocol: "TCP"})
			}
		}
		if len(s.Ports) > 0 {
			out = append(out, s)
		}
	}
	return out, nil
}

func (s service) portsText() string {
	parts := make([]string, len(s.Ports))
	for i, p := range s.Ports {
		parts[i] = strconv.Itoa(p.Port)
		if p.Name != "" {
			parts[i] += "/" + p.Name
		}
	}
	return strings.Join(parts, " ")
}

// freePort returns want if it is free on localhost, otherwise a random free port.
func freePort(want int) int {
	if want > 1024 {
		if l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(want)); err == nil {
			l.Close()
			return want
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return want
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// kpf: pick a service (and port) in the current namespace and port-forward
// it in a pane next to this one.
func (a *app) kpf(filter string) error {
	c, err := kube.Current(a.cfg, "", "")
	if err != nil {
		return err
	}
	ns := c.Namespace
	if ns == "" {
		ns = "default"
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}

	svcs := map[string]service{}
	it, err := tui.Run(tui.Options{
		Title: fmt.Sprintf("Port-forward · services in %s/%s", c.Short, ns),
		Query: filter,
		Load: func() ([]tui.Item, error) {
			out, err := cl.Get(c.Name, "get", "services", "-n", ns, "-o", "json")
			if err != nil {
				return nil, err
			}
			ss, err := parseServices(out)
			if err != nil {
				return nil, err
			}
			if len(ss) == 0 {
				return nil, fmt.Errorf("no TCP services in %s", ns)
			}
			w := 0
			for _, s := range ss {
				w = max(w, len(s.Name))
			}
			items := make([]tui.Item, 0, len(ss))
			for _, s := range ss {
				svcs[s.Name] = s
				items = append(items, tui.Item{Key: s.Name, Label: fmt.Sprintf("%-*s", w, s.Name),
					Hint: fmt.Sprintf("%-12s %s", s.Type, s.portsText())})
			}
			return items, nil
		},
	})
	if err != nil || it == nil {
		return err
	}
	s := svcs[it.Key]

	port := s.Ports[0]
	if len(s.Ports) > 1 {
		opts := make([]tui.Item, len(s.Ports))
		for i, p := range s.Ports {
			opts[i] = tui.Item{Key: strconv.Itoa(i), Label: fmt.Sprintf("%-6d", p.Port), Hint: p.Name}
		}
		ch, err := tui.Run(tui.Options{Title: "Port of " + s.Name, Items: opts})
		if err != nil || ch == nil {
			return err
		}
		i, _ := strconv.Atoi(ch.Key)
		port = s.Ports[i]
	}

	local := freePort(port.Port)
	mapping := fmt.Sprintf("%d:%d", local, port.Port)
	if rex.Block() == "" {
		return a.pftail(c.Name, ns, s.Name, mapping)
	}
	argv := []string{filepath.Join(a.binDir, "rex8s"), "pftail", c.Name, ns, s.Name, mapping}
	if err := rex.Split(fmt.Sprintf("pf %s :%d", s.Name, local), argv...); err != nil {
		return err
	}
	fmt.Printf("port-forward %s → %s\n", tui.Bold.Render("svc/"+s.Name+":"+strconv.Itoa(port.Port)),
		tui.Ok.Render(fmt.Sprintf("localhost:%d", local)))
	return nil
}

// pftail runs the port-forward in its own pane until closed.
func (a *app) pftail(ctxName, ns, svc, mapping string) error {
	c, err := a.context(ctxName)
	if err != nil {
		return err
	}
	// "auto:PORT" (from Raycast): pick a free local port now
	if l, remote, ok := strings.Cut(mapping, ":"); ok && l == "auto" {
		n, _ := strconv.Atoi(remote)
		mapping = fmt.Sprintf("%d:%s", freePort(n), remote)
	}
	local, _, _ := strings.Cut(mapping, ":")
	rex.Rename(fmt.Sprintf("pf %s/%s :%s", c.Short, svc, local))
	a.tint(c)
	bin, err := a.kubectl()
	if err != nil {
		return err
	}
	fmt.Printf("%s  %s → %s\n%s\n\n",
		tui.Bold.Render("port-forward"), "svc/"+svc+" ("+c.Short+"/"+ns+")",
		tui.Ok.Render("http://localhost:"+local), tui.Dim.Render("close this pane (or ctrl+c) to stop"))
	cmd := exec.Command(bin, "--context", ctxName, "-n", ns, "port-forward", "svc/"+svc, mapping)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = setEnv(os.Environ(), "KUBECONFIG", a.src)
	_ = cmd.Run()
	fmt.Fprint(os.Stderr, tui.Dim.Render("\nport-forward ended · press enter to close"))
	bufio.NewReader(os.Stdin).ReadString('\n')
	return nil
}
