package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vitalbh/rex8s/internal/guard"
)

// rcFiles are the shell startup files that can export KUBECONFIG.
var rcFiles = []string{".zshenv", ".zprofile", ".zshrc", ".bash_profile", ".bashrc", ".profile", ".config/fish/config.fish"}

var kubeconfigExport = regexp.MustCompile(`(^|[;&|\s])(export\s+KUBECONFIG=|set\s+(-\w+\s+)*KUBECONFIG\s)`)

// rcKubeconfigExports finds the lines of home's shell startup files that
// export KUBECONFIG for every shell ("~/.zshrc:391"). A rex8s pane starts
// its shell with KUBECONFIG led by the pane's own file, which fixes the
// pane's cluster; such a line replaces it, and kubectl in the pane then uses
// the kubeconfig's current context. A line that mentions REX8S, or sits in an if
// that does (a few lines up, before its fi/end), is guarded and left out.
func rcKubeconfigExports(home string) []string {
	var out []string
	for _, f := range rcFiles {
		b, err := os.ReadFile(filepath.Join(home, f))
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			t := strings.TrimSpace(l)
			if strings.HasPrefix(t, "#") || !kubeconfigExport.MatchString(t) || strings.Contains(t, "REX8S") {
				continue
			}
			guarded := false
			for j := i - 1; j >= max(0, i-3); j-- { // up to an if that mentions REX8S, not past its end
				u := strings.TrimSpace(lines[j])
				if u == "fi" || u == "end" || strings.HasPrefix(u, "fi;") {
					break
				}
				if strings.Contains(u, "REX8S") && !strings.HasPrefix(u, "#") {
					guarded = true
					break
				}
			}
			if !guarded {
				out = append(out, fmt.Sprintf("~/%s:%d", f, i+1))
			}
		}
	}
	return out
}

// doctorShell: nothing in the shell's startup replaces a pane's KUBECONFIG.
func doctorShell(d *checker) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	fix := `export it only outside panes: [ -z "$REX8S" ] && export KUBECONFIG=…`
	if lines := rcKubeconfigExports(home); len(lines) > 0 {
		d.check(false, false, "KUBECONFIG exported for every shell at "+strings.Join(lines, ", "),
			"in a cluster pane kubectl would ignore the pane's cluster; "+fix)
	} else {
		d.check(true, false, "shell startup files leave a pane's KUBECONFIG alone", "")
	}
	if msg := guard.PaneDrift(); msg != "" {
		d.check(false, false, "this pane's KUBECONFIG was replaced after it opened", fix)
	}
}
