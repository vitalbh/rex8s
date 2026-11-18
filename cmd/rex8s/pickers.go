// The cluster and namespace pickers, with the cluster preview.

package main

import (
	"fmt"
	"strings"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

const restoreKey = "\x00restore"

func (a *app) pickCluster(title, selected string) (*kube.Context, error) {
	c, _, err := a.pickClusterOr(title, selected, nil)
	return c, err
}

// pickClusterOr is pickCluster with extra items on top; when one of them is
// chosen its Key is returned instead of a context.
func (a *app) pickClusterOr(title, selected string, extra []tui.Item) (*kube.Context, string, error) {
	raw, err := kube.Load(a.src)
	if err != nil {
		return nil, "", err
	}
	ctxs := kube.Contexts(raw, a.cfg)
	if len(ctxs) == 0 {
		return nil, "", fmt.Errorf("no visible contexts in %s", a.src)
	}
	cl, err := a.client(a.src)
	if err != nil {
		return nil, "", err
	}
	byKey := map[string]kube.Context{}
	extraHint := map[string]string{}
	items := make([]tui.Item, 0, len(ctxs)+len(extra))
	for _, e := range extra {
		extraHint[e.Key] = e.Hint
		items = append(items, e)
	}
	for _, c := range ctxs {
		byKey[c.Name] = c
		badge := "dev"
		if c.Prod {
			badge = "PROD"
		}
		items = append(items, tui.Item{Key: c.Name, Label: fmt.Sprintf("%-7s", c.Short), Hint: c.Name, Badge: badge})
	}
	it, err := tui.Run(tui.Options{
		Title:    title,
		Items:    items,
		Selected: selected,
		Prefetch: true,
		PreviewHead: func(it tui.Item) string {
			if h, ok := extraHint[it.Key]; ok {
				return tui.Bold.Render(it.Label) + "\n\n" + h
			}
			return clusterHead(byKey[it.Key])
		},
		PreviewBody: func(it tui.Item) string {
			if _, ok := extraHint[it.Key]; ok {
				return ""
			}
			return clusterBody(cl, byKey[it.Key])
		},
	})
	if err != nil || it == nil {
		return nil, "", err
	}
	if _, ok := extraHint[it.Key]; ok {
		return nil, it.Key, nil
	}
	c := byKey[it.Key]
	return &c, "", nil
}

func clusterHead(c kube.Context) string {
	tag := tui.Ok.Render("dev")
	if c.Prod {
		tag = tui.Prod.Render(" PROD ")
	}
	ns := c.Namespace
	if ns == "" {
		ns = "default"
	}
	server := strings.TrimPrefix(strings.TrimPrefix(c.Server, "https://"), "http://")
	row := func(k, v string) string { return tui.Dim.Render(fmt.Sprintf("%-10s", k)) + v }
	return strings.Join([]string{
		tui.Bold.Render(c.Short) + "  " + tag,
		"",
		row("context", c.Name),
		row("server", server),
		row("user", c.User),
		row("namespace", ns),
	}, "\n")
}

func clusterBody(cl kube.Client, c kube.Context) string {
	// Sequential on purpose: two concurrent calls against the same OIDC cluster
	// may refresh the token at the same time and invalidate the refresh token.
	// (Different clusters run in parallel: each item is a separate Cmd.)
	nss, nsErr := cl.Namespaces(c.Name)
	var flux kube.FluxStatus
	var fluxErr error
	if nsErr == nil {
		flux, fluxErr = cl.Flux(c.Name)
	}

	if nsErr != nil {
		if kube.AuthExpired(nsErr) {
			return tui.Warn.Render("login expired") + tui.Dim.Render(" · press enter to log in")
		}
		return accessError(c, nsErr)
	}
	row := func(k, v string) string { return tui.Dim.Render(fmt.Sprintf("%-11s", k)) + v }
	lines := []string{row("namespaces", fmt.Sprint(len(nss)))}

	switch {
	case fluxErr != nil:
		lines = append(lines, row("flux", tui.Err.Render(fluxErr.Error())))
	case !flux.Installed:
		lines = append(lines, row("flux", tui.Dim.Render("not installed")))
	default:
		parts := []string{tui.Ok.Render(fmt.Sprintf("%d ready", flux.Ready))}
		if flux.Failing > 0 {
			parts = append(parts, tui.Err.Render(fmt.Sprintf("%d failing", flux.Failing)))
		}
		if flux.Progressing > 0 {
			parts = append(parts, tui.Warn.Render(fmt.Sprintf("%d reconciling", flux.Progressing)))
		}
		if flux.Suspended > 0 {
			parts = append(parts, tui.Warn.Render(fmt.Sprintf("%d suspended", flux.Suspended)))
		}
		lines = append(lines, row("flux", strings.Join(parts, tui.Dim.Render(" · "))))
		for i, p := range flux.Problems {
			if i == 6 {
				lines = append(lines, tui.Dim.Render(fmt.Sprintf("  … and %d more", len(flux.Problems)-i)))
				break
			}
			st := tui.Err
			if p.Suspended {
				st = tui.Warn
			}
			lines = append(lines, "", st.Render(fmt.Sprintf("%s %s/%s", p.Kind, p.Namespace, p.Name)))
			if p.Message != "" && !p.Suspended {
				lines = append(lines, tui.Muted.Render("  "+truncate(oneLine(p.Message), 160)))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func (a *app) pickNamespace(title string, cl kube.Client, ctxName, current string, withDefault bool) (string, bool, error) {
	it, err := tui.Run(tui.Options{
		Title:    title,
		Selected: current,
		Load: func() ([]tui.Item, error) {
			var items []tui.Item
			if withDefault {
				items = append(items, tui.Item{Key: "", Label: "(context default)", Dim: true})
			}
			items = append(items, tui.Item{Key: kube.AllNamespaces, Label: "* all namespaces",
				Hint: "kubectl get/describe/top add -A; commands on one object ask for -n"})
			nss, err := cl.Namespaces(ctxName)
			if err != nil {
				if withDefault {
					items[0].Hint = "could not list namespaces: " + err.Error()
					return items, nil
				}
				return nil, err
			}
			for _, n := range nss {
				items = append(items, tui.Item{Key: n, Label: n})
			}
			return items, nil
		},
	})
	if err != nil || it == nil {
		return "", false, err
	}
	return it.Key, true, nil
}
