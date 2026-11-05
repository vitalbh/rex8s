// Package pathx finds the real binaries (kubectl, helm, k9s...) while skipping
// rex8s's own wrappers, and fills in the minimal PATH that Rex panes get when
// opened with shell = "none".
package pathx

import (
	"os"

	"github.com/vitalbh/rex8s/internal/demo"
	"os/exec"
	"path/filepath"
	"strings"
)

var extraDirs = []string{"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin"}

// Fix prepends common directories and, if kubectl is still missing, appends
// the login shell's PATH.
func Fix(skipDir string) {
	home, _ := os.UserHomeDir()
	dirs := append([]string{}, extraDirs...)
	dirs = append(dirs, filepath.Join(home, ".local", "bin"), filepath.Join(home, "go", "bin"))
	dirs = append(dirs, filepath.SplitList(os.Getenv("PATH"))...)
	set(dirs)
	demo.PathFirst()

	if Real("kubectl", skipDir) == "" {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/zsh"
		}
		out, err := exec.Command(sh, "-lic", `printf %s "$PATH"`).Output()
		if err == nil {
			set(append(filepath.SplitList(os.Getenv("PATH")), filepath.SplitList(string(out))...))
		}
	}
}

func set(dirs []string) {
	seen := map[string]bool{}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	os.Setenv("PATH", strings.Join(out, string(os.PathListSeparator)))
}

// Real looks up name in PATH, skipping skipDir (where the wrappers live).
func Real(name, skipDir string) string {
	skip := canon(skipDir)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || (skip != "" && canon(dir) == skip) {
			continue
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// Prepend returns PATH with dir in front.
func Prepend(dir string) string {
	return dir + string(os.PathListSeparator) + os.Getenv("PATH")
}

func canon(p string) string {
	if p == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}
