package desktop

import (
	"bytes"
	"strings"
	"testing"
)

func swap(t *testing.T, os string) (*bytes.Buffer, *[]string) {
	t.Helper()
	var buf bytes.Buffer
	var calls []string
	oldOut, oldOS, oldRun := Out, goos, run
	Out, goos = &buf, os
	run = func(in, name string, args ...string) error {
		calls = append(calls, strings.TrimSpace(in+" "+name+" "+strings.Join(args, " ")))
		return nil
	}
	t.Cleanup(func() { Out, goos, run = oldOut, oldOS, oldRun })
	return &buf, &calls
}

func TestLinuxPrints(t *testing.T) {
	out, calls := swap(t, "linux")
	if err := OpenURL("http://localhost:3000"); err != nil {
		t.Fatal(err)
	}
	if err := Copy("postgres://localhost:5432"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "http://localhost:3000\npostgres://localhost:5432\n" || len(*calls) != 0 || CanOpen() {
		t.Errorf("out %q, calls %v", out, *calls)
	}
}

func TestMacRuns(t *testing.T) {
	out, calls := swap(t, "darwin")
	OpenURL("http://localhost:3000")
	Copy("x")
	if out.Len() != 0 || strings.Join(*calls, "|") != "open http://localhost:3000|x pbcopy" || !CanOpen() {
		t.Errorf("out %q, calls %v", out, *calls)
	}
}
