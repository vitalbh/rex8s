// rex8s-guard TOOL ARGS... — the kubectl/helm/flux wrapper used in rex8s
// panes: asks for confirmation before mutating production. A separate binary
// with no TUI dependencies, so it doesn't slow down every kubectl.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/demo"
	"github.com/vitalbh/rex8s/internal/guard"
	"github.com/vitalbh/rex8s/internal/pathx"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: rex8s-guard kubectl|helm|flux ARGS...")
		os.Exit(2)
	}
	demo.Apply()
	exe, _ := os.Executable()
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	binDir := filepath.Dir(exe)
	pathx.Fix(binDir)
	cfg, err := config.Load()
	if err == nil {
		err = guard.Run(os.Args[1], os.Args[2:], binDir, cfg)
	}
	fmt.Fprintln(os.Stderr, "rex8s-guard:", err)
	os.Exit(1)
}
