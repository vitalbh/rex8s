package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vitalbh/rex8s/internal/demo"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
)

// offerLogin: a cluster's login expired while a view that keeps running
// reads it (kview, kstatus -w, kwarn -A, the logs, the Flux monitor). The
// Rex palette asks — on the Mac or the iPhone — whether to log in again,
// and kauth opens in a new tab. In the background, never blocking the view;
// at most once per cluster every loginAskEvery, across every rex8s process
// (several views of a cluster expire at once).
const loginAskEvery = 15 * time.Minute

// loginAsk is the question itself; tests replace it.
var loginAsk = func(c kube.Context) {
	a, err := rex.Ask(rex.Prompt{
		Title:   "Login to " + c.Short + " expired",
		Timeout: 600,
		Items: []rex.Item{
			{ID: "login", Title: "Log in again", Subtitle: "kauth " + c.Short + " in a new tab", Icon: "person"},
			{ID: "later", Title: "Not now", Subtitle: "kauth " + c.Short + " any time", Icon: "clock"},
		},
	})
	if err == nil && a.Value == "login" {
		_ = rex.Do("k8s_kauth", map[string]any{"ctx": c.Name})
	}
}

func (a *app) offerLogin(c kube.Context) {
	if a.cache == "" || demo.Active() {
		return
	}
	dir := filepath.Join(a.cache, "login-asked")
	mark := filepath.Join(dir, strings.ReplaceAll(c.Short, "/", "_"))
	if fi, err := os.Stat(mark); err == nil && time.Since(fi.ModTime()) < loginAskEvery {
		return
	}
	if os.MkdirAll(dir, 0o700) != nil || os.WriteFile(mark, nil, 0o600) != nil {
		return
	}
	now := time.Now()
	_ = os.Chtimes(mark, now, now)
	go loginAsk(c)
}
