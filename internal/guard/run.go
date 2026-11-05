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
// command mutates a production cluster. Without a TTY, or with REX8S_FORCE,
// it just passes through.
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

	// REX8S_AGENT: an agent (kask's Claude) runs the command — no terminal,
	// but production changes must not pass silently.
	agent := os.Getenv("REX8S_AGENT") != ""
	if os.Getenv("REX8S_FORCE") != "" || (!agent && !term.IsTerminal(int(os.Stdin.Fd()))) {
		return run()
	}
	p := Parse(args)
	if !Risky(tool, p) {
		return run()
	}
	c, err := kube.Current(cfg, p.Context, p.Kubeconfig)
	if err != nil || !c.Prod {
		return run()
	}
	if agent {
		fmt.Fprintf(os.Stderr, "rex8s guard: refused: %s %s would change the PRODUCTION cluster %s.\n"+
			"Ask the user first. Only if they agree to this exact change, run it again prefixed with REX8S_FORCE=1.\n",
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
