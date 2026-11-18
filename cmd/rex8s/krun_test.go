package main

import (
	"strings"
	"testing"
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
		{[]string{"nginx"}, "nginx", "", "", false},
		{nil, "", "", "", false},
		{[]string{"-c"}, "", "", "", true},
	}
	for _, c := range cases {
		f, cont, cmd, err := parseKrunArgs(c.args)
		if (err != nil) != c.wantErr || f != c.filter || cont != c.cont || strings.Join(cmd, " ") != c.command {
			t.Errorf("%v: got %q %q %q %v", c.args, f, cont, cmd, err)
		}
	}
}
