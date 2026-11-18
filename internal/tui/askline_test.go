package tui

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestAskLine(t *testing.T) {
	var out strings.Builder
	oldOut, oldOpen := askOut, openTTY
	t.Cleanup(func() { askOut, openTTY = oldOut, oldOpen })
	askOut = &out

	openTTY = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("  timeout|refused \nnext\n")), nil
	}
	if got, ok := AskLine("> "); !ok || got != "timeout|refused" || out.String() != "> " {
		t.Errorf("AskLine = %q, %v; printed %q", got, ok, out.String())
	}

	openTTY = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("no newline")), nil }
	if got, ok := AskLine(""); !ok || got != "no newline" {
		t.Errorf("AskLine at EOF = %q, %v", got, ok)
	}

	openTTY = func() (io.ReadCloser, error) { return nil, errors.New("no tty") }
	if got, ok := AskLine(""); ok || got != "" {
		t.Errorf("AskLine without a terminal = %q, %v", got, ok)
	}
}
