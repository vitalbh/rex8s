package guard

import (
	"strings"
	"testing"
)

func TestAllNamespacesArgs(t *testing.T) {
	cases := []struct {
		tool, cmd string
		want      string // "" = refused with a message
	}{
		{"kubectl", "get pods", "get pods -A"},
		{"kubectl", "get pods,svc -o wide", "get pods,svc -o wide -A"},
		{"kubectl", "get pods -n api", "get pods -n api"},
		{"kubectl", "get pods -A", "get pods -A"},
		{"kubectl", "get nodes", "get nodes"},
		{"kubectl", "get ns", "get ns"},
		{"kubectl", "get pod api-1", ""},
		{"kubectl", "get deploy/api", ""},
		{"kubectl", "describe pods", "describe pods -A"},
		{"kubectl", "describe node n1", "describe node n1"},
		{"kubectl", "top pods", "top pods -A"},
		{"kubectl", "top nodes", "top nodes"},
		{"kubectl", "events", "events -A"},
		{"kubectl", "logs api-1", ""},
		{"kubectl", "logs api-1 -n api", "logs api-1 -n api"},
		{"kubectl", "exec -it api-1 -- sh", ""},
		{"kubectl", "delete pod x", ""},
		{"kubectl", "delete node n1", "delete node n1"},
		{"kubectl", "version", "version"},
		{"kubectl", "config get-contexts", "config get-contexts"},
		{"helm", "list", "list -A"},
		{"helm", "status x", ""},
		{"helm", "repo update", "repo update"},
		{"flux", "get ks", "get ks -A"},
		{"flux", "reconcile ks apps", "reconcile ks apps"},
	}
	for _, c := range cases {
		args := strings.Fields(c.cmd)
		out, msg := AllNamespacesArgs(c.tool, Parse(c.tool, args), args)
		got := strings.Join(out, " ")
		if msg != "" {
			got = ""
		}
		if got != c.want {
			t.Errorf("%s %s → %q (msg %q), want %q", c.tool, c.cmd, got, msg, c.want)
		}
	}
}
