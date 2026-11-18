// Package testkube is a fake kubectl for tests: a shell script that answers
// each call by its context and arguments with a given stdout, stderr, exit
// code and timing, and records every call.
//
//	k := testkube.New(t)
//	k.On("use", "get pods *", testkube.Reply{Stdout: podsJSON})
//	k.On("euw", "*", testkube.Reply{Stderr: "error: You must be logged in to the server (Unauthorized)", Exit: 1})
//	cl := kube.Client{Kubectl: k.Path, Kubeconfig: k.Kubeconfig}
//
// It does not import internal/kube, so kube's own tests can use it.
package testkube

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Reply is how the fake answers a call, in this order: Hold starts, Delay
// passes, Shell runs, Stdout and Stderr are written, After passes, and it
// exits with Exit.
type Reply struct {
	Stdout, Stderr string
	Exit           int
	// Delay is how long it waits before answering (a slow API server).
	Delay time.Duration
	// After is how long it keeps running once it answered (a stream such as
	// logs -f or get -w).
	After time.Duration
	// Hold starts a child process that keeps stdout and stderr open this
	// long, also after the fake exits (a credential plugin left behind).
	Hold time.Duration
	// Shell is extra sh code, run with the call's arguments in "$@"; $D is
	// the fake's directory, for files a test shares with it.
	Shell string
}

// Kubectl is a fake kubectl. Answers can change while a test runs: On with
// a pattern already given replaces its reply, and the next call sees it.
type Kubectl struct {
	// Path is the script, to use as the kubectl binary.
	Path string
	// Kubeconfig is a path in the fake's directory (no file is written).
	Kubeconfig string
	// Dir is the fake's directory ($D in Reply.Shell).
	Dir string

	t     testing.TB
	mu    sync.Mutex
	rules []rule
	def   Reply
	seq   int
}

type rule struct {
	ctx, args string
	reply     Reply
}

// New is a fake whose calls all fail (exit 1, "error: testkube: no answer
// for ARGS") until On or Default say otherwise.
func New(t testing.TB) *Kubectl {
	t.Helper()
	dir := t.TempDir()
	k := &Kubectl{Path: filepath.Join(dir, "kubectl"), Kubeconfig: filepath.Join(dir, "kubeconfig"), Dir: dir, t: t,
		def: Reply{Exit: 1, Shell: `echo "error: testkube: no answer for $*" >&2`}}
	k.write()
	return k
}

// On answers the calls whose context matches ctx and whose arguments after
// "--context NAME" (joined by spaces) match args, both shell glob patterns
// where only * and ? are special: "*" matches anything, "" only a call
// without --context. The first matching pattern pair, in the order first
// given, wins.
func (k *Kubectl) On(ctx, args string, r Reply) *Kubectl {
	k.mu.Lock()
	defer k.mu.Unlock()
	for i := range k.rules {
		if k.rules[i].ctx == ctx && k.rules[i].args == args {
			k.rules[i].reply = r
			k.writeLocked()
			return k
		}
	}
	k.rules = append(k.rules, rule{ctx, args, r})
	k.writeLocked()
	return k
}

// Default answers the calls no On pattern matches.
func (k *Kubectl) Default(r Reply) *Kubectl {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.def = r
	k.writeLocked()
	return k
}

// Calls is every call's arguments so far, joined by spaces ("--context use
// get pods -A -o json --request-timeout=9s"), in the order they started.
func (k *Kubectl) Calls() []string {
	data, err := os.ReadFile(filepath.Join(k.Dir, "calls"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// Count is how many calls contained substr ("" counts them all).
func (k *Kubectl) Count(substr string) int {
	n := 0
	for _, c := range k.Calls() {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

func (k *Kubectl) write() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.writeLocked()
}

// writeLocked writes the script for the current rules. Replies live in
// files of their own, never rewritten (a call running the previous script
// still reads its own), and the script is replaced with a rename.
func (k *Kubectl) writeLocked() {
	k.t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "#!/bin/sh\nD=%s\nexport D\n", quote(k.Dir))
	b.WriteString(`printf '%s\n' "$*" >> "$D/calls"
ctx=
args="$*"
if [ "$1" = --context ]; then
	ctx=$2
	shift 2
	args="$*"
	set -- --context "$ctx" "$@"
fi
`)
	for _, r := range k.rules {
		fmt.Fprintf(&b, "case \"$ctx\" in %s) case \"$args\" in %s)\n%s;; esac;; esac\n", glob(r.ctx), glob(r.args), k.replyLocked(r.reply))
	}
	b.WriteString(k.replyLocked(k.def))

	tmp := filepath.Join(k.Dir, fmt.Sprintf("kubectl.%d", k.seq))
	k.seq++
	if err := os.WriteFile(tmp, []byte(b.String()), 0o755); err != nil {
		k.t.Fatal(err)
	}
	if err := os.Rename(tmp, k.Path); err != nil {
		k.t.Fatal(err)
	}
}

// replyLocked is the sh code for r, ending in exit.
func (k *Kubectl) replyLocked(r Reply) string {
	var b strings.Builder
	if r.Hold > 0 {
		fmt.Fprintf(&b, "sleep %s </dev/null &\n", secs(r.Hold))
	}
	if r.Delay > 0 {
		fmt.Fprintf(&b, "sleep %s\n", secs(r.Delay))
	}
	if r.Shell != "" {
		b.WriteString(r.Shell + "\n")
	}
	for _, s := range []struct{ text, redirect string }{{r.Stdout, ""}, {r.Stderr, " >&2"}} {
		if s.text == "" {
			continue
		}
		f := filepath.Join(k.Dir, fmt.Sprintf("reply.%d", k.seq))
		k.seq++
		if err := os.WriteFile(f, []byte(s.text), 0o644); err != nil {
			k.t.Fatal(err)
		}
		fmt.Fprintf(&b, "cat %s%s\n", quote(f), s.redirect)
	}
	if r.After > 0 {
		fmt.Fprintf(&b, "sleep %s\n", secs(r.After))
	}
	fmt.Fprintf(&b, "exit %d\n", r.Exit)
	return b.String()
}

func secs(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) }

// quote single-quotes s for sh.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// glob is p as a case pattern: * and ? stay special, the rest is quoted.
// "" matches only the empty string.
func glob(p string) string {
	if p == "" {
		return "''"
	}
	var b strings.Builder
	lit := ""
	flush := func() {
		if lit != "" {
			b.WriteString(quote(lit))
			lit = ""
		}
	}
	for _, r := range p {
		if r == '*' || r == '?' {
			flush()
			b.WriteRune(r)
			continue
		}
		lit += string(r)
	}
	flush()
	return b.String()
}
