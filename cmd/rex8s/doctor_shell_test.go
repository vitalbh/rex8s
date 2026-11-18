package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestRcKubeconfigExports(t *testing.T) {
	home := t.TempDir()
	write := func(name, body string) {
		p := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".zshrc", `# export KUBECONFIG=~/.kube/old
export PATH=$PATH:/x
export KUBECONFIG="$HOME/.kube/config:$HOME/.kube/elan.yaml"
[ -z "$REX8S" ] && export KUBECONFIG="$HOME/.kube/config"
if [ -z "$REX8S" ]; then
  export KUBECONFIG=~/.kube/b
fi
alias k=kubectl; export KUBECONFIG=~/.kube/c
`)
	write(".bashrc", "echo $KUBECONFIG\n")
	write(".config/fish/config.fish", "set -gx KUBECONFIG ~/.kube/config\n")
	got := rcKubeconfigExports(home)
	want := []string{"~/.zshrc:3", "~/.zshrc:8", "~/.config/fish/config.fish:1"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
