package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kauth logs in again to a cluster whose credentials expired, for setups
// where a login page (e.g. a Dex authenticator) prints `kubectl config ...`
// commands to paste. It opens the page, takes the pasted commands, runs them
// against the master kubeconfig (even from inside a pane) and checks access.
//
//	kauth [CLUSTER]   short name or context; default: this pane's cluster
func (a *app) kauth(args []string) error {
	fs := flag.NewFlagSet("kauth", flag.ContinueOnError)
	force := fs.Bool("f", false, "log in even if the current login still works")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := a.resolveCluster(fs.Arg(0))
	if err != nil {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}

	if !*force {
		fmt.Print(tui.Dim.Render("checking " + c.Short + "… "))
		if _, err := cl.Get(c.Name, "auth", "whoami", "-o", "name"); err == nil || !kube.AuthExpired(err) {
			if err == nil {
				fmt.Println(tui.Ok.Render("login is fine") + tui.Dim.Render(" (use -f to log in anyway)"))
				return nil
			}
			fmt.Println(tui.Err.Render("unreachable, but not a login problem: " + truncate(err.Error(), 150)))
			return nil
		}
		fmt.Println(tui.Warn.Render("login expired"))
	}
	return a.login(c)
}

// ensureLogin runs the login flow when c's credentials expired, so picking a
// logged-out cluster leads straight to logging in. Other errors are left for
// the caller to surface.
func (a *app) ensureLogin(cl kube.Client, c kube.Context) error {
	_, err := cl.Namespaces(c.Name) // cached when the picker preview already asked
	if err == nil || !kube.AuthExpired(err) {
		return nil
	}
	fmt.Print("\x1b[H\x1b[2J") // clear what the picker left behind
	fmt.Println(tui.Warn.Render("login expired for " + c.Short))
	return a.login(c)
}

// login opens the login page, applies the pasted `kubectl config` commands to
// the master kubeconfig and checks access.
func (a *app) login(c kube.Context) error {
	if url := a.cfg.Login(c.Server); url != "" {
		fmt.Println("opening " + tui.Bold.Render(url))
		_ = exec.Command("open", url).Run()
	} else {
		fmt.Println(tui.Dim.Render("tip: set login_url in " + a.cfg.File() + " and kauth will open the login page for you"))
	}
	fmt.Printf("\nLog in for %s and copy everything the page shows (mkdir, cat and the\nkubectl config commands), then paste it here. It is applied when the paste ends.\n\n",
		tui.Bold.Render(c.Short+" ("+c.Name+")"))

	restore := pasteMode()
	text := readPaste(os.Stdin)
	time.Sleep(200 * time.Millisecond) // let the rest of the paste arrive, then drop it
	flushInput()
	restore()
	home, _ := os.UserHomeDir()
	steps, err := parseScript(text, home)
	if err != nil {
		return err
	}
	bin, err := a.kubectl()
	if err != nil {
		return err
	}
	fmt.Println()
	for _, st := range steps {
		switch st.kind {
		case stepMkdir:
			err = os.MkdirAll(st.path, 0o700)
		case stepWrite:
			err = os.WriteFile(st.path, []byte(st.content), 0o600)
		case stepKubectl:
			cmd := exec.Command(bin, st.argv[1:]...)
			cmd.Env = setEnv(os.Environ(), "KUBECONFIG", a.src) // the master, never a pane file
			if out, e := cmd.CombinedOutput(); e != nil {
				err = errors.New(strings.TrimSpace(string(out)))
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", st, err)
		}
		fmt.Println(tui.Ok.Render("  ok ") + tui.Dim.Render(st.String()))
	}

	nss, err := (kube.Client{Kubectl: bin, Kubeconfig: a.src}).Namespaces(c.Name)
	if err != nil {
		return errors.New("still no access to " + c.Short + ": " + err.Error())
	}
	fmt.Println(tui.Ok.Render(fmt.Sprintf("%s is back: %d namespaces", c.Short, len(nss))))
	return nil
}

// resolveCluster finds a context by name or short name; "" = current context.
func (a *app) resolveCluster(name string) (kube.Context, error) {
	if name == "" {
		return kube.Current(a.cfg, "", "")
	}
	raw, err := kube.Load(a.src)
	if err != nil {
		return kube.Context{}, err
	}
	if c, ok := kube.Find(raw, a.cfg, name); ok {
		return c, nil
	}
	for _, c := range kube.Contexts(raw, a.cfg) {
		if c.Short == name {
			return c, nil
		}
	}
	return kube.Context{}, fmt.Errorf("no context or cluster named %q", name)
}

// shellSplit splits a command line like a POSIX shell would for quoting
// (single quotes, double quotes, backslash escapes). No expansion is done.
func shellSplit(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord, quote := false, rune(0)
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch {
			case r == '"':
				quote = 0
			case r == '\\' && i+1 < len(rs) && strings.ContainsRune(`"\$`+"`", rs[i+1]):
				i++
				cur.WriteRune(rs[i])
			default:
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == '\\' && i+1 < len(rs):
			i++
			cur.WriteRune(rs[i])
			inWord = true
		case r == ' ' || r == '\t':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote in pasted command")
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}
