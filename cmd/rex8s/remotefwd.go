package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/vitalbh/rex8s/internal/rex"
)

// On a Linux Rex server the Mac's Rex app forwards a localhost port to the
// Mac by itself when you click a link to it, which covers the HTTP URLs kpf
// prints. A database port has no link to click, and rex8s cannot set up
// that forward from here: a Rex forward listens on the machine of the
// client that carries it (`rex forward new -L` run on the server would
// listen on the server itself), the tunnel blocks it makes belong to that
// client, and the app's own "Forward Port…" is a dialog with no arguments.
// So rex8s says the one command to run on the Mac instead.

// remoteRex: rex8s runs in a Rex pane on a server that isn't the Mac
// running the app (the app's own server only runs on macOS).
var remoteRex = func() bool { return rex.Block() != "" && runtime.GOOS != "darwin" }

// serverName is how the Mac most likely names this server in Rex: its host
// name, without the domain.
var serverName = func() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "SERVER"
	}
	h, _, _ = strings.Cut(h, ".")
	return h
}

// remoteForwardHint is the line kpf prints for a port the Mac can't reach
// by a click: "" unless in a remote Rex session.
func remoteForwardHint(local int) string {
	if !remoteRex() {
		return ""
	}
	return fmt.Sprintf("on the Mac: rex -S %s forward new -L %d:127.0.0.1:%d  (or Forward Port… in Rex's palette)",
		serverName(), local, local)
}
