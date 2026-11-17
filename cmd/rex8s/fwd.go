package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/kube"
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

// favKind: what a favorite speaks — the web unless its port is a known
// database or broker (favorites were made for web UIs; a path means web).
func favKind(f config.Forward) portKind {
	if f.Path != "" {
		return kindHTTP
	}
	if k := detectKind(svcPort{Port: f.Port}); k != kindTCP {
		return k
	}
	return kindHTTP
}

// favoriteURL: favURL for the web, the client URL (postgres://…) otherwise.
func favoriteURL(f config.Forward, local int) string {
	if k := favKind(f); !k.web() {
		return k.url(local)
	}
	return favURL(local, f.Path)
}

func favURL(local int, path string) string {
	if path != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return fmt.Sprintf("http://localhost:%d%s", local, path)
}

// kpfFavorite: kpf NAME — the favorite's forward (started in the background
// when it doesn't run), opened in the browser.
func (a *app) kpfFavorite(f config.Forward) error { return a.fwd(f.Name) }

// fwd NAME (kpf NAME, Raycast): start the favorite's background forward
// unless it runs, then open its URL in the browser.
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
	kind := favKind(f)
	e, reused, err := a.lazyEnsure(c, nsOr(f.Namespace), f.Service, svcPort{Port: f.Port, Protocol: "TCP", Name: string(kind)}, f.Local)
	if err != nil {
		return err
	}
	url := favoriteURL(f, e.Local)
	if reused {
		fmt.Println("already running → " + tui.Ok.Render(url))
	} else {
		fmt.Println(tui.Bold.Render(f.Name) + " → " + tui.Ok.Render(url) + tui.Dim.Render("  (kubectl connects on first use)"))
	}
	if !kind.web() { // a database: nothing to open; the client command helps
		if cl := kind.client(e.Local); cl != "" {
			fmt.Println("  " + cl)
		}
		return nil
	}
	return exec.Command("open", url).Run()
}

// kpfFavorites: picker of the favorites (running ones marked).
func (a *app) kpfFavorites() error {
	if len(a.cfg.Forwards) == 0 {
		return fmt.Errorf("no favorite forwards: add \"forwards:\" to %s (see docs/configuration.md)", a.cfg.File())
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
	Web       bool   `json:"web"` // opens in a browser (else: copy the URL)
}

// favoritesJSON for the Raycast menu bar (rex8s list fwd).
func (a *app) favoritesJSON() []favoriteJSON {
	out := []favoriteJSON{}
	for _, f := range a.cfg.Forwards {
		j := favoriteJSON{Name: f.Name, Cluster: f.Cluster, Namespace: nsOr(f.Namespace), Service: f.Service, Port: f.Port,
			Web: favKind(f).web()}
		if c, err := a.clusterContext(f.Cluster); err == nil {
			j.Prod = c.Prod
			if e, ok := a.runningForward(f, c); ok {
				j.Running, j.URL = true, favoriteURL(f, e.Local)
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
