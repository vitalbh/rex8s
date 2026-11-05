// Package rex talks to Rex through its CLI: renames the current pane and runs actions.
package rex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
)

var appBins = []string{
	"/Applications/Rex.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex",
	"/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex",
}

// Bin finds the Rex CLI: PATH first (inside Rex it is already there), then the app bundle.
func Bin() string {
	if p, err := exec.LookPath("rex"); err == nil {
		return p
	}
	for _, p := range appBins {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "rex"
}

// Block is the current pane (REX_BLOCK), or "" outside Rex.
func Block() string { return os.Getenv("REX_BLOCK") }

// Rename sets the current pane's label. A no-op outside Rex.
func Rename(label string) {
	b := Block()
	if b == "" {
		return
	}
	_ = exec.Command(Bin(), "block", "rename", b, label).Run()
}

// RenameWindow sets the name of the tab (window) containing the current pane.
func RenameWindow(label string) {
	b := Block()
	if b == "" {
		return
	}
	out, err := exec.Command(Bin(), "block", "inspect", b).Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if w, ok := strings.CutPrefix(line, "window:"); ok {
			_ = exec.Command(Bin(), "window", "rename", strings.TrimSpace(w), label).Run()
			return
		}
	}
}

// Do runs an init.lua action with JSON arguments.
func Do(action string, args map[string]any) error {
	j, err := json.Marshal(args)
	if err != nil {
		return err
	}
	out, err := exec.Command(Bin(), "do", action, "--args", string(j)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("rex do %s: %v: %s", action, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DoAndFocus runs an action that opens a window and makes sure that window ends
// up in front. Called from a floating picker, the new window is activated
// first and then loses focus when the picker's layer closes (the process
// exits) and Rex returns focus to the window underneath. So a detached
// helper focuses the new window again once the picker is gone.
func DoAndFocus(action string, args map[string]any) error {
	s := Session()
	before, _ := windowIDs(s)
	if err := Do(action, args); err != nil {
		return err
	}
	if s == "" || before == nil {
		return nil
	}
	after, err := windowIDs(s)
	if err != nil {
		return nil
	}
	for _, id := range after {
		if !slices.Contains(before, id) {
			focusLater(s, id)
			break
		}
	}
	return nil
}

// windowIDs lists the session's window IDs (nil when it can't).
func windowIDs(session string) ([]string, error) {
	if session == "" {
		return nil, nil
	}
	out, err := exec.Command(Bin(), "window", "ls", "-s", session, "--json").Output()
	if err != nil {
		return nil, err
	}
	var v struct {
		Windows []struct {
			ID string `json:"window_id"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	ids := []string{}
	for _, w := range v.Windows {
		ids = append(ids, w.ID)
	}
	return ids, nil
}

// focusLater focuses a window from a detached process, after this one exits.
func focusLater(session, window string) {
	cmd := exec.Command("/bin/sh", "-c", `sleep 0.4; exec "$0" focus -s "$1" "$2"`, Bin(), session, window)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}

// Session is the current session (REX_SESSION), or "" outside Rex.
func Session() string { return os.Getenv("REX_SESSION") }

// SetTheme sets the current pane's background color. "" restores the default.
//
// It writes OSC 11 (OSC 111 to reset) to the controlling terminal. Rex's
// set_theme API only applies for the client that owns the block (the app), so
// a call from the pane's own process would be queued and never take effect.
func SetTheme(background string) {
	if Block() == "" {
		return
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer tty.Close()
	fmt.Fprint(tty, themeSeq(background))
}

func themeSeq(background string) string {
	if background == "" {
		return "\x1b]111\x07"
	}
	if !strings.HasPrefix(background, "#") {
		background = "#" + background
	}
	return "\x1b]11;" + background + "\x07"
}

// Split opens a new pane next to the current one running argv.
func Split(label string, argv ...string) error {
	args := []string{"split", "--shell=none", "--label", label}
	if b := Block(); b != "" {
		args = append(args, "-b", b)
	}
	args = append(args, "--")
	args = append(args, argv...)
	out, err := exec.Command(Bin(), args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("rex split: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Status reports program status (OSC 7501) to the terminal: state is idle,
// working, done, blocked, error or clear. id may be "" for the root record.
func Status(id, state, title, msg string) {
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return
	}
	defer tty.Close()
	pairs := []string{"state=" + state, "app=rex8s"}
	if id != "" {
		pairs = append(pairs, "id="+id)
	}
	if title != "" {
		pairs = append(pairs, "title="+b64(title))
	}
	if msg != "" {
		pairs = append(pairs, "msg="+b64(msg))
	}
	fmt.Fprintf(tty, "\x1b]7501;%s\x1b\\", strings.Join(pairs, ":"))
}

// b64 encodes a single-line value for OSC 7501 (control characters removed,
// capped well below the protocol limits).
func b64(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// Notifications turns Notify on or off (config: notifications).
var Notifications = true

// Notify shows a macOS notification. Best effort.
func Notify(title, msg string) {
	if !Notifications {
		return
	}
	script := fmt.Sprintf("display notification %s with title %s", appleQuote(msg), appleQuote(title))
	_ = exec.Command("osascript", "-e", script).Run()
}

func appleQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// Blocks returns the IDs (without the "block:" prefix) of every live block in
// every session. ok is false when Rex can't be asked, so callers don't treat
// "unknown" as "gone".
func Blocks() (ids map[string]bool, ok bool) { return BlocksTimeout(10 * time.Second) }

// BlocksTimeout is Blocks with a connection timeout (e.g. while Rex shuts down).
func BlocksTimeout(d time.Duration) (ids map[string]bool, ok bool) {
	out, err := exec.Command(Bin(), "--timeout", d.String(), "--autostart=false", "block", "ls", "-A", "-q", "--short=false").Output()
	if err != nil {
		return nil, false
	}
	ids = map[string]bool{}
	for _, l := range strings.Fields(string(out)) {
		ids[strings.TrimPrefix(l, "block:")] = true
	}
	return ids, len(ids) > 0
}
