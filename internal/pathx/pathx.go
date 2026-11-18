// Package pathx finds the real binaries (kubectl, helm, k9s...) while skipping
// rex8s's own wrappers, and fills in the minimal PATH that Rex panes get when
// opened with shell = "none".
package pathx

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/vitalbh/rex8s/internal/demo"
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

// Real looks up name in PATH, skipping skipDir (where the wrappers live) and
// anything that is rex8s itself: a link to rex8s or rex8s-guard (the
// kubectl -> rex8s-guard shim, wherever its directory is), this very
// program, or an old rex8s wrapper script. So the guard never finds itself.
func Real(name, skipDir string) string {
	skip := canon(skipDir)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" || (skip != "" && canon(dir) == skip) {
			continue
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 && !isRex8s(p, fi) {
			return p
		}
	}
	return ""
}

// wrapperMark is in every wrapper script older rex8s versions wrote.
var wrapperMark = []byte("# rex8s wrapper")

// isRex8s: p (fi is its target's info) is rex8s, rex8s-guard, this program,
// or an old wrapper script.
func isRex8s(p string, fi os.FileInfo) bool {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		if b := filepath.Base(r); b == "rex8s" || b == "rex8s-guard" {
			return true
		}
	}
	if self, err := os.Executable(); err == nil {
		if si, err := os.Stat(self); err == nil && os.SameFile(si, fi) {
			return true
		}
	}
	if fi.Size() < 512 {
		if data, err := os.ReadFile(p); err == nil && bytes.Contains(data, wrapperMark) {
			return true
		}
	}
	return false
}

// SelfDir is the directory this program was started from, without
// resolving links: for ~/.config/rex/rex8s/bin/kview -> rex8s -> Homebrew's
// opt path, the wrapper dir (where the k* commands and the kubectl shim are),
// not the Cellar. A candidate counts only when it is this very program and
// its directory holds both rex8s and rex8s-guard; otherwise (go test, a
// binary copied alone) it is the resolved executable's directory.
func SelfDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	resolved := exe
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		resolved = r
	}
	self, err := os.Stat(resolved)
	if err != nil {
		return filepath.Dir(resolved)
	}
	var cands []string
	if a := os.Args[0]; strings.Contains(a, "/") {
		cands = append(cands, a)
	} else if p, err := exec.LookPath(a); err == nil {
		cands = append(cands, p)
	}
	cands = append(cands, exe) // macOS: the path as run; Linux: resolved
	for _, c := range cands {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if fi, err := os.Stat(abs); err != nil || !os.SameFile(fi, self) {
			continue
		}
		dir := filepath.Dir(abs)
		if isFile(filepath.Join(dir, "rex8s")) && isFile(filepath.Join(dir, "rex8s-guard")) {
			return dir
		}
	}
	return filepath.Dir(resolved)
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
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
