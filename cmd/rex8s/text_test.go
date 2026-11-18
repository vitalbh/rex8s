package main

import (
	"strings"
	"testing"
)

func TestShellSplit(t *testing.T) {
	got, err := shellSplit(`kubectl config set-credentials "my user" --auth-provider-arg='id-token=a b' --x=c\ d`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"kubectl", "config", "set-credentials", "my user", "--auth-provider-arg=id-token=a b", "--x=c d"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", got)
	}
	if _, err := shellSplit(`kubectl "open`); err == nil {
		t.Fatal("unterminated quote must fail")
	}
}

func TestMatchWords(t *testing.T) {
	for _, c := range []struct {
		text, filter string
		want         bool
	}{
		{"use monitoring/grafana", "grafana", true},
		{"use monitoring/grafana", "Grafana USE", true},
		{"use monitoring/grafana", "grafana euw", false},
		{"use monitoring/grafana", "", true},
	} {
		if got := matchWords(c.text, c.filter); got != c.want {
			t.Errorf("matchWords(%q, %q) = %v", c.text, c.filter, got)
		}
	}
}
