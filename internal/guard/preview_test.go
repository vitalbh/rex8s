package guard

import (
	"reflect"
	"strings"
	"testing"
)

func TestPreviewArgs(t *testing.T) {
	for _, c := range []struct {
		cmd, want string
		diff      bool
	}{
		{"kubectl --context use apply -f x.yaml", "--context use diff -f x.yaml", true},
		{"kubectl --context use -n api replace -f x.yaml", "--context use -n api diff -f x.yaml", true},
		{"kubectl --context use create -f x.yaml", "--context use diff -f x.yaml", true},
		{"kubectl --context use create deployment web --image nginx", "", false},
		{"kubectl --context use delete pod web-1 --grace-period 0 --force", "--context use get pod web-1", false},
		{"kubectl --context use delete -f x.yaml --wait=false", "--context use get -f x.yaml", false},
		{"kubectl --context use apply -f x.yaml --dry-run=server", "", false},
		{"kubectl --context use scale deploy web --replicas 2", "", false},
		{"helm upgrade web ./chart", "", false},
	} {
		f := strings.Fields(c.cmd)
		got, diff := previewArgs(f[0], Parse(f[0], f[1:]), f[1:])
		if strings.Join(got, " ") != c.want || diff != c.diff {
			t.Errorf("%s: got %q diff=%v, want %q diff=%v", c.cmd, strings.Join(got, " "), diff, c.want, c.diff)
		}
	}
}

func TestDiffStat(t *testing.T) {
	d := "diff -u -N a b\n--- a\n+++ b\n@@ -1,2 +1,2 @@\n-replicas: 1\n+replicas: 2\n+x: y\n context"
	if got := diffStat(d); got != "+2 −1" {
		t.Errorf("diffStat = %q", got)
	}
	if got := diffStat("NAME READY\nweb-1 1/1"); got != "" {
		t.Errorf("not a diff: %q", got)
	}
	if !reflect.DeepEqual(dropDeleteFlags([]string{"pod", "x", "--timeout", "5s", "--now"}), []string{"pod", "x"}) {
		t.Error("dropDeleteFlags")
	}
}
