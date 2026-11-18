// rex8s-guard TOOL ARGS... — the kubectl/helm/flux wrapper used in rex8s
// panes: asks for confirmation before mutating production. setup links
// kubectl, helm and flux to it; run under one of those names it is that tool
// (kubectl get pods = rex8s-guard kubectl get pods). A separate binary with
// no TUI dependencies, so it doesn't slow down every kubectl.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/demo"
	"github.com/vitalbh/rex8s/internal/guard"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/pathx"
)

// version is set at build time (-ldflags "-X main.version=v1.2.3").
var version = "dev"

func main() {
	if tool, args, ok := shimArgs(os.Args); ok { // run as kubectl, helm or flux
		run(tool, args)
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "--prompt" {
		prompt(os.Args[2:])
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: rex8s-guard kubectl|helm|flux ARGS...")
		os.Exit(2)
	}
	run(os.Args[1], os.Args[2:])
}

// shimArgs: run through a link named kubectl, helm or flux (kubectl ->
// rex8s-guard, which setup installs), the name is the tool and every
// argument is the tool's: kubectl get pods = rex8s-guard kubectl get pods.
func shimArgs(argv []string) (tool string, args []string, ok bool) {
	if len(argv) == 0 {
		return "", nil, false
	}
	switch b := filepath.Base(argv[0]); b {
	case "kubectl", "helm", "flux":
		return b, argv[1:], true
	}
	return "", nil, false
}

// run hands tool and args to the guard; it only returns on an error (the
// tool replaces this process).
func run(tool string, args []string) {
	demo.Apply()
	binDir := pathx.SelfDir()
	pathx.Fix(binDir)
	cfg, err := config.Load()
	if err == nil {
		err = guard.Run(tool, args, binDir, cfg)
	}
	fmt.Fprintln(os.Stderr, "rex8s-guard:", err)
	os.Exit(1)
}

// prompt prints the current cluster for a shell prompt — fast: it reads the
// kubeconfig and the rex8s config only, never the cluster.
//
//	rex8s-guard --prompt [zsh|plain]
//
// Production shows as "PROD dal/ns" in red; others as "dal/ns" dimmed;
// nothing when there is no current context.
func prompt(args []string) {
	format := "zsh"
	if len(args) > 0 {
		format = args[0]
	}
	demo.Apply()
	cfg, err := config.Load()
	if err != nil {
		return
	}
	c, err := kube.Current(cfg, "", "")
	if err != nil || c.Name == "" {
		return
	}
	text := c.Short
	switch {
	case c.AllNS:
		text += "/*"
	case c.Namespace != "" && c.Namespace != "default":
		text += "/" + c.Namespace
	}
	switch {
	case format == "plain" && c.Prod:
		fmt.Print("PROD " + text)
	case format == "plain":
		fmt.Print(text)
	case c.Prod: // zsh prompt escapes
		fmt.Print("%B%F{red}PROD " + text + "%f%b")
	default:
		fmt.Print("%F{244}" + text + "%f")
	}
}
