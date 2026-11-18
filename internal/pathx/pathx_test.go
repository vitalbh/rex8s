package pathx

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, p, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o755); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, target, p string) {
	t.Helper()
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
}

// The wrapper dir holds kubectl -> rex8s-guard; Real must find the real
// kubectl after it, whether or not it is told to skip that dir, and when
// rex8s-guard is itself a link into a Homebrew Cellar.
func TestRealSkipsTheGuard(t *testing.T) {
	root := t.TempDir()
	cellar := filepath.Join(root, "Cellar", "rex8s", "1.0", "bin")
	opt := filepath.Join(root, "opt", "rex8s", "bin")
	wrap := filepath.Join(root, "wrap")
	real := filepath.Join(root, "real")
	write(t, filepath.Join(cellar, "rex8s-guard"), "#!/bin/sh\n")
	write(t, filepath.Join(cellar, "rex8s"), "#!/bin/sh\n")
	write(t, filepath.Join(real, "kubectl"), "#!/bin/sh\necho real\n")
	if err := os.MkdirAll(filepath.Join(root, "opt", "rex8s"), 0o755); err != nil {
		t.Fatal(err)
	}
	link(t, cellar, opt) // opt/rex8s/bin -> Cellar/rex8s/1.0/bin
	if err := os.MkdirAll(wrap, 0o755); err != nil {
		t.Fatal(err)
	}
	link(t, filepath.Join(opt, "rex8s-guard"), filepath.Join(wrap, "rex8s-guard"))
	link(t, filepath.Join(opt, "rex8s"), filepath.Join(wrap, "rex8s"))
	link(t, "rex8s-guard", filepath.Join(wrap, "kubectl"))
	link(t, "rex8s", filepath.Join(wrap, "kview"))

	t.Setenv("PATH", wrap+string(os.PathListSeparator)+real)
	want := filepath.Join(real, "kubectl")
	for _, skip := range []string{wrap, cellar, ""} {
		if got := Real("kubectl", skip); got != want {
			t.Errorf("Real(kubectl, %q) = %q, want %q", skip, got, want)
		}
	}
	if got := Real("kview", ""); got != "" {
		t.Errorf("Real(kview) = %q: a link to rex8s is not a real tool", got)
	}

	// an old wrapper script (older rex8s) in a directory not skipped
	old := filepath.Join(root, "old")
	write(t, filepath.Join(old, "kubectl"), "#!/bin/sh\n# rex8s wrapper\nexec \"$(dirname \"$0\")/rex8s-guard\" kubectl \"$@\"\n")
	t.Setenv("PATH", old+string(os.PathListSeparator)+real)
	if got := Real("kubectl", cellar); got != want {
		t.Errorf("old script: Real = %q, want %q", got, want)
	}

	// nothing but the guard: not found, rather than itself
	t.Setenv("PATH", wrap)
	if got := Real("kubectl", ""); got != "" {
		t.Errorf("only the guard on PATH: Real = %q, want \"\"", got)
	}
}
