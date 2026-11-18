package main

import (
	"os"

	"golang.org/x/term"

	"github.com/vitalbh/rex8s/internal/forward"
)

// newLogLinker returns the forward.Linker for a log stream of ctx/ns, or nil
// (lines stay as they are) when links would not help: stdout is not a
// terminal (| grep, > file), log_links is off, or there is no kubectl.
func (a *app) newLogLinker(ctx, ns string) *forward.Linker {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return nil
	}
	mode := a.cfg.LogLinksMode()
	if mode == "off" {
		return nil
	}
	cl, err := a.client(a.src)
	if err != nil {
		return nil
	}
	return &forward.Linker{Client: cl, Context: ctx, Namespace: ns, AllNamespaces: mode == "all"}
}
