package guard

import (
	"strings"
	"testing"
)

func TestRisky(t *testing.T) {
	cases := []struct {
		tool string
		args []string
		want bool
	}{
		{"kubectl", []string{"get", "pods"}, false},
		{"kubectl", []string{"-n", "api", "delete", "pod", "x"}, true},
		{"kubectl", []string{"--context", "prod", "apply", "-f", "a.yaml"}, true},
		{"kubectl", []string{"rollout", "status", "deploy/x"}, false},
		{"kubectl", []string{"rollout", "restart", "deploy/x"}, true},
		{"kubectl", []string{"logs", "-f", "x"}, false},
		{"kubectl", []string{"exec", "-it", "x", "--", "delete"}, false},
		{"helm", []string{"--kube-context", "prod", "upgrade", "x", "."}, true},
		{"helm", []string{"list", "-A"}, false},
		{"flux", []string{"reconcile", "ks", "apps"}, false},
		{"flux", []string{"suspend", "hr", "api"}, true},
	}
	for _, tc := range cases {
		if got := Risky(tc.tool, Parse(tc.args)); got != tc.want {
			t.Errorf("%s %v: Risky=%v, want %v", tc.tool, tc.args, got, tc.want)
		}
	}
}

func TestParseFlags(t *testing.T) {
	p := Parse([]string{"--context=prod", "--kubeconfig", "/k", "-n", "api", "delete", "pod", "x"})
	if p.Context != "prod" || p.Kubeconfig != "/k" || p.Verb != "delete" || p.Sub != "pod" {
		t.Fatalf("Parse = %+v", p)
	}
}

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
		out, msg := AllNamespacesArgs(c.tool, Parse(args), args)
		got := strings.Join(out, " ")
		if msg != "" {
			got = ""
		}
		if got != c.want {
			t.Errorf("%s %s → %q (msg %q), want %q", c.tool, c.cmd, got, msg, c.want)
		}
	}
}
