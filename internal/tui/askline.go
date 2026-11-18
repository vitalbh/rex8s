package tui

import (
	"bufio"
	"io"
	"os"
	"strings"
)

// Where AskLine writes and reads; tests swap them.
var (
	askOut  io.Writer = os.Stderr
	openTTY           = func() (io.ReadCloser, error) { return os.Open("/dev/tty") }
)

// AskLine prints prompt on stderr and reads one line from the terminal
// (/dev/tty, so it works with stdin redirected), without the surrounding
// spaces. ok is false when there is no terminal to read from.
func AskLine(prompt string) (line string, ok bool) {
	io.WriteString(askOut, prompt)
	tty, err := openTTY()
	if err != nil {
		return "", false
	}
	defer tty.Close()
	l, _ := bufio.NewReader(tty).ReadString('\n')
	return strings.TrimSpace(l), true
}
