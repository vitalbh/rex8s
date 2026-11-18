package guard

import (
	"strings"
	"testing"
)

// Inside a pane, KUBECONFIG must start with the pane's file under the cache
// dir; a shell rc that set it again gets a warning. Outside a pane, nothing.
func TestPaneDrift(t *testing.T) {
	old := sys
	t.Cleanup(func() { sys = old })
	for _, c := range []struct {
		rex8s, kubeconfig string
		warn              bool
	}{
		{"", "/home/u/.kube/config", false},                         // not a pane
		{"1", "/c/rex8s/block-1.yaml:/home/u/.kube/config", false},  // the pane's file first
		{"1", "/home/u/.kube/config:/home/u/.kube/elan.yaml", true}, // an rc exported it again
		{"1", "", true}, // or unset it
		{"1", "/c/rex8s-other/x.yaml:/home/u/.kube/config", true}, // a look-alike dir
	} {
		env := map[string]string{"REX8S": c.rex8s, "KUBECONFIG": c.kubeconfig, "XDG_CACHE_HOME": "/c"}
		sys.getenv = func(k string) string { return env[k] }
		got := PaneDrift()
		if (got != "") != c.warn || (c.warn && !strings.Contains(got, `[ -z "$REX8S" ]`)) {
			t.Errorf("REX8S=%q KUBECONFIG=%q: %q", c.rex8s, c.kubeconfig, got)
		}
	}
}
