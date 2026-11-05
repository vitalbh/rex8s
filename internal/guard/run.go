package guard

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/pathx"
)

// Run executes tool (kubectl/helm/flux), asking for confirmation when the
// command mutates a production cluster. Without a TTY (agents, scripts) such
// a command is refused; REX8S_FORCE=1 lets it through.
//
// It lives outside the TUI binary on purpose: Bubble Tea's init() queries the
// terminal background color and can block for up to 5s on terminals that
// don't answer — unacceptable in a wrapper that runs on every kubectl.
func Run(tool string, args []string, skipDir string, cfg *config.Config) error {
	bin := pathx.Real(tool, skipDir)
	if bin == "" {
		return fmt.Errorf("%s not found in PATH", tool)
	}
	run := func() error { return syscall.Exec(bin, append([]string{tool}, args...), os.Environ()) }

	// a pane showing every namespace: reads get -A, the rest needs -n
	if p0 := Parse(args); true {
		if c, err := kube.Current(cfg, p0.Context, p0.Kubeconfig); err == nil && c.AllNS {
			adapted, msg := AllNamespacesArgs(tool, p0, args)
			if msg != "" {
				fmt.Fprintln(os.Stderr, "rex8s: "+msg)
				os.Exit(1)
			}
			args = adapted
		}
	}
	if os.Getenv("REX8S_FORCE") != "" {
		return run()
	}
	// Nobody at a terminal to confirm: an agent (Claude Code sets CLAUDECODE,
	// kask sets REX8S_AGENT; Codex, Cursor and scripts have no TTY). A change
	// to production is refused instead of passing silently.
	unattended := os.Getenv("REX8S_AGENT") != "" || os.Getenv("CLAUDECODE") == "1" || !term.IsTerminal(int(os.Stdin.Fd()))
	p := Parse(args)
	if !Risky(tool, p) {
		return run()
	}
	c, err := kube.Current(cfg, p.Context, p.Kubeconfig)
	if err != nil || !c.Prod {
		return run()
	}
	if unattended {
		fmt.Fprintf(os.Stderr, "rex8s guard: refused: %s %s would change the PRODUCTION cluster %s, and there is no terminal to confirm it.\n"+
			"An agent: ask the user first; only if they agree to this exact change, run it again prefixed with REX8S_FORCE=1.\n"+
			"A script: prefix it with REX8S_FORCE=1.\n",
			tool, strings.Join(args, " "), c.Short)
		os.Exit(1)
	}

	var notes []string
	if FluxReverts(tool, p) {
		notes = append(notes, "reminder: with Flux, manual changes to managed resources are reverted on the next reconcile.")
	}
	if !Confirm(c.Short, tool+" "+strings.Join(args, " "), notes...) {
		os.Exit(1)
	}
	return run()
}

// Confirm shows the red PRODUCTION banner for cluster short and asks the user
// to type its name. It reads from /dev/tty, so it works even with stdin
// redirected. Returns false (after printing "cancelled.") on any other answer.
func Confirm(short, what string, notes ...string) bool {
	color := term.IsTerminal(int(os.Stderr.Fd()))
	paint := func(code, s string) string {
		if !color {
			return s
		}
		return "\x1b[" + code + "m" + s + "\x1b[0m"
	}
	fmt.Fprintln(os.Stderr, paint("1;97;41", " PRODUCTION ("+short+") ")+" "+what)
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, paint("38;5;214", "   "+n))
	}
	fmt.Fprintf(os.Stderr, "   type '%s' to confirm: ", short)
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	defer tty.Close()
	ans, _ := bufio.NewReader(tty).ReadString('\n')
	if strings.TrimSpace(ans) != short {
		fmt.Fprintln(os.Stderr, "cancelled.")
		return false
	}
	return true
}
