package main

import (
	"errors"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// Favorite port-forwards (config "forwards"): kpf NAME starts one in a
// split and opens the browser when the port answers; when it already runs,
// only the browser opens. kpf -f picks one.

func (a *app) favorite(name string) (config.Forward, bool) {
	for _, f := range a.cfg.Forwards {
		if strings.EqualFold(f.Name, name) {
			return f, true
		}
	}
	return config.Forward{}, false
}

// clusterContext resolves a short name or a context name.
func (a *app) clusterContext(name string) (kube.Context, error) {
	raw, err := kube.Load(a.src)
	if err != nil {
		return kube.Context{}, err
	}
	for _, c := range kube.Contexts(raw, a.cfg) {
		if c.Name == name {
			return c, nil
		}
	}
	for _, c := range kube.Contexts(raw, a.cfg) {
		if c.Short == name {
			return c, nil
		}
	}
	return kube.Context{}, fmt.Errorf("cluster %q not found (short name or context)", name)
}

// running returns the port-forward already serving this favorite.
func (a *app) runningForward(f config.Forward, c kube.Context) (pfEntry, bool) {
	for _, e := range a.pfList() {
		if e.Context == c.Name && e.Namespace == f.Namespace && e.Service == f.Service && e.Remote == f.Port {
			return e, true
		}
	}
	return pfEntry{}, false
}

func favURL(local int, path string) string {
	if path != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return fmt.Sprintf("http://localhost:%d%s", local, path)
}

// kpfFavorite: open it if running, else start it (split, or this pane).
func (a *app) kpfFavorite(f config.Forward) error {
	c, err := a.clusterContext(f.Cluster)
	if err != nil {
		return err
	}
	if e, ok := a.runningForward(f, c); ok {
		url := favURL(e.Local, f.Path)
		fmt.Println("already running → " + tui.Ok.Render(url))
		return exec.Command("open", url).Run()
	}
	if rex.Block() == "" {
		return a.fwd(f.Name)
	}
	return rex.SplitBehind("pf "+f.Name, filepath.Join(a.binDir, "rex8s"), "fwd", f.Name)
}

// fwd runs a favorite's port-forward in this pane and opens the browser once
// the local port accepts connections (used by kpf NAME and Raycast).
func (a *app) fwd(name string) error {
	f, ok := a.favorite(name)
	if !ok {
		return fmt.Errorf("no favorite forward %q in %s", name, a.cfg.File())
	}
	c, err := a.clusterContext(f.Cluster)
	if err != nil {
		return err
	}
	if f.Service == "" || f.Port == 0 {
		return errors.New("forward " + name + ": service and port are required")
	}
	ns := nsOr(f.Namespace)
	if e, ok := a.runningForward(f, c); ok { // started meanwhile (e.g. twice from Raycast)
		return exec.Command("open", favURL(e.Local, f.Path)).Run()
	}
	want := f.Local
	if want == 0 {
		want = f.Port
	}
	local := freePort(want)
	url := favURL(local, f.Path)
	go func() {
		for i := 0; i < 60; i++ {
			if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(local), 300*time.Millisecond); err == nil {
				conn.Close()
				_ = exec.Command("open", url).Run()
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
	return a.pftail(c.Name, ns, f.Service, fmt.Sprintf("%d:%d", local, f.Port))
}

// kpfFavorites: picker of the favorites (running ones marked).
func (a *app) kpfFavorites() error {
	if len(a.cfg.Forwards) == 0 {
		return fmt.Errorf("no favorite forwards: add \"forwards:\" to %s (see README)", a.cfg.File())
	}
	w := 0
	for _, f := range a.cfg.Forwards {
		w = max(w, len(f.Name))
	}
	var items []tui.Item
	for _, f := range a.cfg.Forwards {
		badge := ""
		if c, err := a.clusterContext(f.Cluster); err == nil {
			if _, ok := a.runningForward(f, c); ok {
				badge = "ON"
			}
		}
		items = append(items, tui.Item{Key: f.Name, Label: fmt.Sprintf("%-*s", w, f.Name), Badge: badge,
			Hint: fmt.Sprintf("%s %s/%s:%d", f.Cluster, nsOr(f.Namespace), f.Service, f.Port)})
	}
	it, err := tui.Run(tui.Options{Title: "Favorite port-forwards · enter opens (starts it when needed)", Items: items})
	if err != nil || it == nil {
		return err
	}
	f, _ := a.favorite(it.Key)
	return a.kpfFavorite(f)
}

type favoriteJSON struct {
	Name      string `json:"name"`
	Cluster   string `json:"cluster"`
	Prod      bool   `json:"prod"`
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	Port      int    `json:"port"`
	Running   bool   `json:"running"`
	URL       string `json:"url"` // when running
}

// favoritesJSON for the Raycast menu bar (rex8s list fwd).
func (a *app) favoritesJSON() []favoriteJSON {
	out := []favoriteJSON{}
	for _, f := range a.cfg.Forwards {
		j := favoriteJSON{Name: f.Name, Cluster: f.Cluster, Namespace: nsOr(f.Namespace), Service: f.Service, Port: f.Port}
		if c, err := a.clusterContext(f.Cluster); err == nil {
			j.Prod = c.Prod
			if e, ok := a.runningForward(f, c); ok {
				j.Running, j.URL = true, favURL(e.Local, f.Path)
			}
		}
		out = append(out, j)
	}
	return out
}

// saveFavorite adds a running forward to config.yaml's forwards (name
// defaults to the service; taken names get the cluster appended).
func (a *app) saveFavorite(e pfEntry, name string) (string, error) {
	if name == "" {
		name = e.Service
	}
	if _, taken := a.favorite(name); taken {
		if name == e.Service {
			name = e.Service + "-" + e.Short
		}
		if _, taken := a.favorite(name); taken {
			return "", fmt.Errorf("there is already a favorite named %q", name)
		}
	}
	for _, f := range a.cfg.Forwards {
		if c, err := a.clusterContext(f.Cluster); err == nil && c.Name == e.Context && f.Namespace == e.Namespace && f.Service == e.Service && f.Port == e.Remote {
			return f.Name, fmt.Errorf("already a favorite: %s", f.Name)
		}
	}
	item := fmt.Sprintf("{ name: %s, cluster: %s, namespace: %s, service: %s, port: %d",
		yamlScalar(name), yamlScalar(e.Short), yamlScalar(e.Namespace), yamlScalar(e.Service), e.Remote)
	if e.Local != e.Remote {
		item += fmt.Sprintf(", local: %d", e.Local)
	}
	item += " }"
	if err := a.editConfig(func(doc string) string { return addYAMLListItem(doc, "forwards", item) }); err != nil {
		return "", err
	}
	return name, nil
}

// yamlScalar quotes a value when YAML would read it as something else.
func yamlScalar(s string) string {
	if s == "" || strings.ContainsAny(s, ":#{}[],&*!|>'\"%@`") || strings.TrimSpace(s) != s {
		return strconv.Quote(s)
	}
	return s
}

// addYAMLListItem appends "  - item" to the top-level list key, keeping the
// rest of the file (and its comments) as it is. Handles a missing key, an
// empty inline list (key: []) and a block list.
func addYAMLListItem(doc, key, item string) string {
	lines := strings.Split(strings.TrimRight(doc, "\n"), "\n")
	if doc == "" {
		lines = nil
	}
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, key+":") {
			start = i
			break
		}
	}
	entry := "  - " + item
	if start < 0 {
		return strings.Join(append(lines, key+":", entry), "\n") + "\n"
	}
	rest := strings.TrimSpace(strings.TrimPrefix(lines[start], key+":"))
	if c := yamlComment(lines[start]); c != "" {
		rest = strings.TrimSpace(strings.TrimSuffix(rest, c))
	}
	if rest == "[]" || rest == "" {
		if rest == "[]" {
			lines[start] = key + ":"
		}
		end := start
		for i := start + 1; i < len(lines); i++ {
			l := lines[i]
			if strings.TrimSpace(l) == "" || strings.HasPrefix(l, " ") || strings.HasPrefix(l, "-") || strings.HasPrefix(l, "#") {
				if strings.TrimSpace(l) != "" && !strings.HasPrefix(strings.TrimSpace(l), "#") {
					end = i
				}
				continue
			}
			break
		}
		out := append([]string{}, lines[:end+1]...)
		out = append(out, entry)
		out = append(out, lines[end+1:]...)
		return strings.Join(out, "\n") + "\n"
	}
	// an inline list with items: leave it alone, add a block list after it
	// would duplicate the key — rewrite the line as a block instead
	inner := strings.TrimSuffix(strings.TrimPrefix(rest, "["), "]")
	lines[start] = key + ":"
	var add []string
	if strings.TrimSpace(inner) != "" {
		add = append(add, "  - "+strings.TrimSpace(inner))
	}
	add = append(add, entry)
	out := append([]string{}, lines[:start+1]...)
	out = append(out, add...)
	out = append(out, lines[start+1:]...)
	return strings.Join(out, "\n") + "\n"
}
