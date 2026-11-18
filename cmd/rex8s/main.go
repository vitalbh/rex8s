// rex8s — one Kubernetes cluster per pane in Rex.
//
// Every subcommand is in commands.go: rex8s help lists them, and in a rex8s
// pane the k* commands (klogs, kpf, kview…) run them.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/demo"
	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/pathx"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

var version = "dev"

type app struct {
	paneRun string // pane --run: a command to run before the interactive shell

	cfg    *config.Config
	binDir string // where rex8s and the wrappers live
	src    string // master kubeconfig
	cache  string // per-pane kubeconfigs
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	c := lookup(cmd)
	if c != nil && c.noApp { // demo: before the config, which it changes
		if err := c.run(nil, args); err != nil {
			fmt.Fprintln(os.Stderr, "rex8s: "+err.Error())
			os.Exit(1)
		}
		return
	}
	demo.Apply()
	a, err := newApp()
	if err == nil {
		err = a.dispatch(cmd, args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, tui.Err.Render("rex8s: "+err.Error()))
		if c != nil && c.pane {
			// runs in a pane that would close immediately: wait so the error can be read
			waitClose("")
		}
		os.Exit(1)
	}
}

func newApp() (*app, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	binDir := filepath.Dir(exe)
	pathx.Fix(binDir)

	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	rex.Notifications = cfg.NotificationsOn()
	if term.IsTerminal(int(os.Stderr.Fd())) {
		for _, w := range cfg.Warnings {
			fmt.Fprintln(os.Stderr, tui.Warn.Render("rex8s: "+tildePath(cfg.File())+": "+w))
		}
	}
	src := os.Getenv("REX8S_SOURCE")
	if src == "" {
		src = cfg.KubeconfigPath()
	}
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		h, _ := os.UserHomeDir()
		cache = filepath.Join(h, ".cache")
	}
	return &app{cfg: cfg, binDir: binDir, src: src, cache: filepath.Join(cache, "rex8s")}, nil
}

func (a *app) kubectl() (string, error) {
	p := pathx.Real("kubectl", a.binDir)
	if p == "" {
		return "", errors.New("kubectl not found in PATH")
	}
	return p, nil
}

func (a *app) client(kubeconfig string) (kube.Client, error) {
	k, err := a.kubectl()
	return kube.Client{Kubectl: k, Kubeconfig: kubeconfig}, err
}

// context resolves a context or short name with fleet.Resolve.
func (a *app) context(name string) (kube.Context, error) {
	raw, err := kube.Load(a.src)
	if err != nil {
		return kube.Context{}, err
	}
	return fleet.Resolve(raw, a.cfg, name)
}

func setEnv(env []string, kv ...string) []string {
	for i := 0; i+1 < len(kv); i += 2 {
		prefix := kv[i] + "="
		out := env[:0:0]
		for _, e := range env {
			if !strings.HasPrefix(e, prefix) {
				out = append(out, e)
			}
		}
		env = append(out, prefix+kv[i+1])
	}
	return env
}
