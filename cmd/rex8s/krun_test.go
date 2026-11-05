package main

import (
	"strings"
	"testing"

	"github.com/vitalbh/rex8s/internal/kube"
)

func TestParseKrunArgs(t *testing.T) {
	cases := []struct {
		args         []string
		filter, cont string
		command      string
		wantErr      bool
	}{
		{[]string{"nginx", "--", "nginx", "-s", "reload"}, "nginx", "", "nginx -s reload", false},
		{[]string{"nginx", "nginx", "-s", "reload"}, "nginx", "", "nginx -s reload", false},
		{[]string{"-c", "proxy", "nginx", "--", "nginx", "-t"}, "nginx", "proxy", "nginx -t", false},
		{[]string{"nginx"}, "", "", "", true},
		{nil, "", "", "", true},
	}
	for _, c := range cases {
		f, cont, cmd, err := parseKrunArgs(c.args)
		if (err != nil) != c.wantErr || f != c.filter || cont != c.cont || strings.Join(cmd, " ") != c.command {
			t.Errorf("%v: got %q %q %q %v", c.args, f, cont, cmd, err)
		}
	}
}

func TestUniqueClusters(t *testing.T) {
	got := uniqueClusters([]kube.Context{
		{Name: "linode", Server: "https://a"}, {Name: "linode2", Server: "https://a"}, {Name: "dal", Server: "https://b"},
	})
	if len(got) != 2 || got[0].Name != "linode" || got[1].Name != "dal" {
		t.Errorf("aliases of the same server should collapse: %+v", got)
	}
}

func TestParseWorkloadSelectors(t *testing.T) {
	ws, err := parseWorkloadSelectors([]byte(`{"items":[
	 {"kind":"Deployment","metadata":{"name":"nginx","namespace":"web"},"spec":{"selector":{"matchLabels":{"tier":"edge","app":"nginx"}}}},
	 {"kind":"DaemonSet","metadata":{"name":"agent","namespace":"ops"},"spec":{"selector":{}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if ws[0].Selector != "app=nginx,tier=edge" || ws[0].Kind != "Deployment" || ws[1].Selector != "" {
		t.Errorf("selectors: %+v", ws)
	}
}
