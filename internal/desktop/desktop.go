// Package desktop opens URLs and copies text on the machine rex8s runs on.
// On macOS that is `open` and `pbcopy`; elsewhere (a Linux server reached
// over SSH) there is no desktop to hand them to, so the text is printed for
// the terminal to show instead.
package desktop

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Out is where URLs and text go when there is no desktop (tests swap it).
var Out io.Writer = os.Stdout

var goos = runtime.GOOS

// run starts name with args (stdin from in, when not "") and waits for it,
// so no zombie process is left behind. Tests swap it.
var run = func(in, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if in != "" {
		cmd.Stdin = strings.NewReader(in)
	}
	return cmd.Run()
}

// OpenURL opens url in the default browser on macOS. Elsewhere it prints
// the URL on Out. It waits for `open`, which returns as soon as the browser
// has the URL: call it in a goroutine from a UI that must not block.
func OpenURL(url string) error {
	if goos != "darwin" {
		_, err := fmt.Fprintln(Out, url)
		return err
	}
	return run("", "open", url)
}

// Copy puts text on the clipboard on macOS. Elsewhere it prints the text on
// Out, for the terminal to copy from.
func Copy(text string) error {
	if goos != "darwin" {
		_, err := fmt.Fprintln(Out, text)
		return err
	}
	return run(text, "pbcopy")
}

// CanOpen reports whether OpenURL opens a browser (else it only prints).
func CanOpen() bool { return goos == "darwin" }
