package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/desktop"
	"github.com/vitalbh/rex8s/internal/forward"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
	"github.com/vitalbh/rex8s/internal/yamledit"
)

// Favorite port-forwards (config "forwards"): kpf NAME starts one in the
// background and opens the browser; when it already runs, only the browser
// opens. kpf -f picks one.

func (a *app) favorite(name string) (config.Forward, bool) {
	for _, f := range a.cfg.Forwards {
		if strings.EqualFold(f.Name, name) {
			return f, true
		}
	}
	return config.Forward{}, false
}

// running returns the port-forward already serving this favorite.
func (a *app) runningForward(f config.Forward, c kube.Context) (forward.Entry, bool) {
	return a.registry().Find(c.Name, nsOr(f.Namespace), f.Service, f.Port)
}

// favKind: what a favorite speaks — the web unless its port is a known
// database or broker (favorites were made for web UIs; a path means web).
func favKind(f config.Forward) forward.Kind {
	if f.Path != "" {
		return forward.KindHTTP
	}
	if k := detectKind(kube.ServicePort{Port: f.Port}); k != forward.KindTCP {
		return k
	}
	return forward.KindHTTP
}

// favoriteURL: favURL for the web, the client URL (postgres://…) otherwise.
func favoriteURL(f config.Forward, local int) string {
	if k := favKind(f); !k.Web() {
		return k.URL(local)
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
	c, err := a.context(f.Cluster)
	if err != nil {
		return err
	}
	if f.Service == "" || f.Port == 0 {
		return errors.New("forward " + name + ": service and port are required")
	}
	kind := favKind(f)
	e, reused, err := a.lazyEnsure(c, nsOr(f.Namespace), f.Service, kube.ServicePort{Port: f.Port, Protocol: "TCP", Name: string(kind)}, f.Local)
	if err != nil {
		return err
	}
	url := favoriteURL(f, e.Local)
	if reused {
		fmt.Println("already running → " + tui.Ok.Render(url))
	} else {
		fmt.Println(tui.Bold.Render(f.Name) + " → " + tui.Ok.Render(url) + tui.Dim.Render("  (kubectl connects on first use)"))
	}
	if !kind.Web() { // a database: nothing to open; the client command helps
		if cl := kind.Client(e.Local); cl != "" {
			fmt.Println("  " + cl)
		}
		return nil
	}
	if !desktop.CanOpen() {
		return nil // the URL is printed above
	}
	return desktop.OpenURL(url)
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
		if c, err := a.context(f.Cluster); err == nil {
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
			Web: favKind(f).Web()}
		if c, err := a.context(f.Cluster); err == nil {
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
func (a *app) saveFavorite(e forward.Entry, name string) (string, error) {
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
		if c, err := a.context(f.Cluster); err == nil && c.Name == e.Context && f.Namespace == e.Namespace && f.Service == e.Service && f.Port == e.Remote {
			return f.Name, fmt.Errorf("already a favorite: %s", f.Name)
		}
	}
	item := fmt.Sprintf("{ name: %s, cluster: %s, namespace: %s, service: %s, port: %d",
		yamledit.String(name), yamledit.String(e.Short), yamledit.String(e.Namespace), yamledit.String(e.Service), e.Remote)
	if e.Local != e.Remote {
		item += fmt.Sprintf(", local: %d", e.Local)
	}
	item += " }"
	if err := a.editConfig(func(doc string) string { return yamledit.AddListItem(doc, "forwards", item) }); err != nil {
		return "", err
	}
	return name, nil
}
