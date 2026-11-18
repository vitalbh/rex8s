package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/vitalbh/rex8s/internal/guard"
	"github.com/vitalbh/rex8s/internal/tui"
)

const defaultUnlock = 15 * time.Minute

// kunlock: for a while, production changes in this pane's cluster (or the
// ones named, or all) go through without typing the cluster name — like
// sudo's grace period — also for agents working for you. Only a person at a
// terminal can unlock.
//
//	kunlock [CLUSTER...|all] [DURATION]   default: this pane's cluster, 15m
//	kunlock off [CLUSTER...|all]          lock again (no cluster: every one)
//	kunlock -l                            what is unlocked
func (a *app) kunlock(args []string) error {
	if len(args) > 0 && (args[0] == "-l" || args[0] == "--list") {
		return listUnlocks()
	}
	off := len(args) > 0 && (args[0] == "off" || args[0] == "lock")
	if off {
		args = args[1:]
	}
	if !off && (os.Getenv("REX8S_AGENT") != "" || os.Getenv("CLAUDECODE") == "1" || !term.IsTerminal(int(os.Stdin.Fd()))) {
		return errors.New("kunlock is for the person at the terminal: agents and scripts can't unlock production")
	}
	d, names, err := parseUnlockArgs(args)
	if err != nil {
		return err
	}
	if off {
		if len(names) == 0 {
			names = []string{guard.AllClusters}
		}
		for _, n := range names {
			short, err := a.unlockTarget(n)
			if err != nil {
				return err
			}
			if err := guard.Lock(short); err != nil {
				return err
			}
		}
		fmt.Println(tui.Ok.Render("locked: ") + strings.Join(names, ", "))
		return nil
	}
	if len(names) == 0 {
		names = []string{""} // this pane's cluster
	}
	var done []string
	for _, n := range names {
		short, err := a.unlockTarget(n)
		if err != nil {
			return err
		}
		if err := guard.Unlock(short, d); err != nil {
			return err
		}
		if short == guard.AllClusters {
			short = "every cluster"
		}
		done = append(done, short)
	}
	until := time.Now().Add(d).Format("15:04")
	fmt.Println(tui.Warn.Render("unlocked: "+strings.Join(done, ", ")) +
		tui.Dim.Render(" — production changes (yours and your agents') go through without asking until "+until+" · kunlock off locks again"))
	return nil
}

// parseUnlockArgs: a duration anywhere (default 15m), the rest cluster
// names.
func parseUnlockArgs(args []string) (time.Duration, []string, error) {
	d := defaultUnlock
	var names []string
	for _, arg := range args {
		if v, err := time.ParseDuration(arg); err == nil {
			if v <= 0 || v > 12*time.Hour {
				return 0, nil, fmt.Errorf("unlock for between 1s and 12h, not %s", arg)
			}
			d = v
			continue
		}
		names = append(names, arg)
	}
	return d, names, nil
}

// unlockTarget: "" is this pane's cluster, "all" every cluster, otherwise a
// cluster's short or context name.
func (a *app) unlockTarget(name string) (string, error) {
	if name == "all" || name == guard.AllClusters {
		return guard.AllClusters, nil
	}
	c, err := a.contextOrCurrent(name)
	if err != nil {
		return "", err
	}
	if name != "" && !c.Prod {
		fmt.Fprintln(os.Stderr, tui.Dim.Render(c.Short+" is not production: the guard doesn't ask there anyway"))
	}
	return c.Short, nil
}

func listUnlocks() error {
	u := guard.Unlocks()
	if len(u) == 0 {
		fmt.Println(tui.Ok.Render("nothing unlocked: production changes ask for the cluster name"))
		return nil
	}
	for _, n := range guard.UnlockedNames() {
		label := n
		if n == guard.AllClusters {
			label = "every cluster"
		}
		fmt.Printf("%s %s\n", tui.Warn.Render(label), tui.Dim.Render("until "+u[n].Format("15:04")))
	}
	return nil
}
