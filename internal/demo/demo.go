// Package demo switches rex8s to fake data for screenshots and recordings:
// a fake kubeconfig (example.com clusters) and fake kubectl/flux/claude/k9s
// from the repo's demo/ directory, so no real cluster data can show up.
//
// It is on when $REX8S_DEMO points at the demo directory, or when
// ~/.config/rex/rex8s/demo contains that path (`rex8s demo on|off`), which
// also covers panes opened by Rex and the Raycast extension.
package demo

import (
	"os"
	"path/filepath"
	"strings"
)

// Marker is the file that turns demo mode on everywhere.
func Marker() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		h, _ := os.UserHomeDir()
		base = filepath.Join(h, ".config")
	}
	return filepath.Join(base, "rex", "rex8s", "demo")
}

// Dir returns the active demo directory, or "".
func Dir() string {
	if d := os.Getenv("REX8S_DEMO"); d != "" {
		return d
	}
	if b, err := os.ReadFile(Marker()); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// Apply points config, kubeconfig and PATH at the demo directory. Call it
// first thing in main; call PathFirst again after anything that edits PATH.
func Apply() bool {
	dir := Dir()
	if dir == "" {
		return false
	}
	os.Setenv("REX8S_DEMO", dir)
	os.Setenv("REX8S_CONFIG", filepath.Join(dir, "config.yaml"))
	os.Setenv("REX8S_SOURCE", filepath.Join(dir, "kubeconfig"))
	os.Setenv("GIT_TERMINAL_PROMPT", "0")
	// never let the real kubeconfig in: keep a pane's "pane.yaml:demo" list,
	// otherwise use the demo kubeconfig alone
	if !strings.Contains(os.Getenv("KUBECONFIG"), dir) {
		os.Setenv("KUBECONFIG", filepath.Join(dir, "kubeconfig"))
	}
	PathFirst()
	return true
}

// PathFirst puts the demo bin directory (fake kubectl & co.) first on PATH.
func PathFirst() {
	dir := os.Getenv("REX8S_DEMO")
	if dir == "" {
		return
	}
	bin := filepath.Join(dir, "bin")
	parts := []string{bin}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p != bin {
			parts = append(parts, p)
		}
	}
	os.Setenv("PATH", strings.Join(parts, string(os.PathListSeparator)))
}

// Active reports whether demo mode is on in this process.
func Active() bool { return os.Getenv("REX8S_DEMO") != "" }
