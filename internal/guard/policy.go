package guard

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/vitalbh/rex8s/internal/kube"
)

// The production policy, the same for the kubectl/helm/flux wrappers (Run)
// and for rex8s commands that change a cluster themselves (ConfirmChange):
//
//  1. REX8S_FORCE set: go ahead (logged as "forced").
//  2. The clusters were unlocked with kunlock (unlock.go): go ahead, for
//     agents too, logged as "unlocked". Only a person can unlock.
//  3. Nobody at a terminal to confirm: an agent (Claude Code sets CLAUDECODE,
//     kask sets REX8S_AGENT; Codex, Cursor and scripts have no TTY). Refused
//     with a message saying how to proceed.
//  4. Otherwise the user types the production cluster name(s).
//
// Every decision is written to the audit log (see audit.go).

var (
	// ErrCancelled: the user was asked and didn't type the cluster name.
	ErrCancelled = errors.New("cancelled")
	// ErrRefused: a production change with no terminal to confirm it.
	ErrRefused = errors.New("refused")
)

// system is what the policy needs from the outside world; tests replace it.
type system struct {
	getenv   func(string) string
	stdinTTY func() bool                           // stdin is a terminal
	openTTY  func() (io.ReadCloser, error)         // where the answer is read from
	stderr   io.Writer                             // where the question goes
	color    func() bool                           // paint the banner
	exec     func(bin string, argv []string) error // replaces the process
}

var sys = system{
	getenv:   os.Getenv,
	stdinTTY: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
	openTTY:  func() (io.ReadCloser, error) { return os.Open("/dev/tty") },
	stderr:   os.Stderr,
	color:    func() bool { return term.IsTerminal(int(os.Stderr.Fd())) },
	exec:     func(bin string, argv []string) error { return syscall.Exec(bin, argv, os.Environ()) },
}

func unattended() bool {
	return sys.getenv("REX8S_AGENT") != "" || sys.getenv("CLAUDECODE") == "1" || !sys.stdinTTY()
}

// decide applies the policy to a change (what) on the production clusters
// prod (short names). maybe: the guard can't tell whether it changes anything.
func decide(prod []string, what string, maybe bool, notes ...string) error {
	who := strings.Join(prod, ",")
	if sys.getenv("REX8S_FORCE") != "" {
		audit(who, "forced", what)
		return nil
	}
	if unlocked(prod) {
		audit(who, "unlocked", what)
		return nil
	}
	if unattended() {
		audit(who, "refused", what)
		verb := "would change"
		if maybe {
			verb = "may change"
		}
		cluster := "cluster"
		if len(prod) > 1 {
			cluster = "clusters"
		}
		return fmt.Errorf("%w: %s %s the PRODUCTION %s %s, and there is no terminal to confirm it.\n"+
			"An agent: ask the user first; only if they agree to this exact change, run it again prefixed with REX8S_FORCE=1\n"+
			"(or the user can run kunlock in their terminal to let changes to this cluster through for a while).\n"+
			"A script: prefix it with REX8S_FORCE=1.",
			ErrRefused, what, verb, cluster, strings.Join(prod, ", "))
	}
	if !ask(prod, what, notes...) {
		audit(who, "cancelled", what)
		return ErrCancelled
	}
	audit(who, "confirmed", what)
	return nil
}

// ConfirmChange applies the production policy to a change a rex8s command
// makes itself, without the kubectl wrapper (kroll, krun, kjunk, kexec…).
// clusters are the ones the change touches; with no production among them it
// returns nil at once. Otherwise REX8S_FORCE lets it through, an agent or a
// missing terminal gets ErrRefused (its message says how to go on), and the
// user types the production cluster names (ErrCancelled on anything else,
// after printing "cancelled.").
func ConfirmChange(clusters []kube.Context, what string, notes ...string) error {
	prod := ProdShorts(clusters)
	if len(prod) == 0 {
		return nil
	}
	return decide(prod, what, false, notes...)
}

// ProdShorts: the short names of the production clusters among cs, sorted,
// each once.
func ProdShorts(cs []kube.Context) []string {
	seen := map[string]bool{}
	var out []string
	for _, c := range cs {
		if c.Prod && !seen[c.Short] {
			seen[c.Short] = true
			out = append(out, c.Short)
		}
	}
	sort.Strings(out)
	return out
}

// Confirm shows the red PRODUCTION banner for cluster short and asks the user
// to type its name; the answer goes to the audit log. It asks even without a
// terminal (and then fails): prefer ConfirmChange, which refuses agents.
func Confirm(short, what string, notes ...string) bool {
	ok := ask([]string{short}, what, notes...)
	outcome := "cancelled"
	if ok {
		outcome = "confirmed"
	}
	audit(short, outcome, what)
	return ok
}

// ask shows the PRODUCTION banner and has the user type the cluster names
// (any order, separated by spaces or commas). It reads from the terminal
// (/dev/tty), so it works even with stdin redirected.
func ask(prod []string, what string, notes ...string) bool {
	color := sys.color()
	paint := func(code, s string) string {
		if !color {
			return s
		}
		return "\x1b[" + code + "m" + s + "\x1b[0m"
	}
	w := sys.stderr
	fmt.Fprintln(w, paint("1;97;41", " PRODUCTION ("+strings.Join(prod, ", ")+") ")+" "+what)
	for _, n := range notes {
		fmt.Fprintln(w, paint("38;5;214", "   "+n))
	}
	fmt.Fprintf(w, "   type '%s' to confirm: ", strings.Join(prod, " "))
	tty, err := sys.openTTY()
	if err != nil {
		fmt.Fprintln(w, "\nno terminal to read the answer from: cancelled.")
		return false
	}
	defer tty.Close()
	ans, _ := bufio.NewReader(tty).ReadString('\n')
	if !sameNames(ans, prod) {
		fmt.Fprintln(w, "cancelled.")
		return false
	}
	return true
}

// sameNames: the answer names exactly the clusters in want.
func sameNames(answer string, want []string) bool {
	got := strings.FieldsFunc(answer, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\n' || r == '\r' })
	if len(got) != len(want) {
		return false
	}
	need := map[string]bool{}
	for _, w := range want {
		need[w] = true
	}
	for _, g := range got {
		if !need[g] {
			return false
		}
		delete(need, g)
	}
	return len(need) == 0
}
