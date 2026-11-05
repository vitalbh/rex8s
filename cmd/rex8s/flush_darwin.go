package main

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// flushInput discards terminal input that hasn't been read yet (e.g. the tail
// of a paste), so it can't turn into keystrokes for the next picker.
func flushInput() {
	const fread = 1 // FREAD in <sys/fcntl.h>
	_ = unix.IoctlSetPointerInt(int(os.Stdin.Fd()), unix.TIOCFLUSH, fread)
}

// pasteMode prepares the terminal for a large paste and returns a function
// that restores it. In canonical mode macOS caps an input line at 1024 bytes
// (MAX_CANON) and silently drops the rest — including the newline — so a long
// token line would hang the read forever. Turning ICANON off lifts the limit
// while keeping echo. Bracketed paste is also switched off so the pasted text
// isn't wrapped in escape sequences.
func pasteMode() func() {
	fmt.Print("\x1b[?2004l")
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, unix.TIOCGETA)
	if err != nil {
		return func() {}
	}
	t := *old
	t.Lflag &^= unix.ICANON
	t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TIOCSETA, &t); err != nil {
		return func() {}
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TIOCSETA, old) }
}
