package guard

import "testing"

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
