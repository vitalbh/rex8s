package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

const rollTimeout = 10 * time.Minute

// kroll restarts the workloads you mark (on any cluster) and follows the
// rollouts until every one is done, with progress in the tab header.
//
//	kroll [FILTER]
//
// --target CTX|KIND|NS|NAME (repeatable) skips the picker (Raycast).
func (a *app) kroll(args []string) error {
	filter := ""
	var given []target
	for i := 0; i < len(args); i++ {
		if args[i] == "--target" && i+1 < len(args) {
			p := strings.SplitN(args[i+1], "|", 4)
			i++
			if len(p) != 4 {
				return fmt.Errorf("--target wants CTX|KIND|NS|NAME, got %q", args[i])
			}
			c, err := a.context(p[0])
			if err != nil {
				return err
			}
			given = append(given, target{c: c, Kind: p[1], Namespace: p[2], Name: p[3]})
			continue
		}
		filter = args[i]
	}
	k, err := a.kubectl()
	if err != nil {
		return err
	}
	targets := given
	if len(targets) == 0 {
		raw, err := kube.Load(a.src)
		if err != nil {
			return err
		}
		clusters := uniqueClusters(kube.Contexts(raw, a.cfg))
		var skipped []string
		targets, _, err = a.pickTargets(k, clusters, filter, "Restart which workloads?", &skipped)
		for _, s := range skipped {
			fmt.Fprintln(os.Stderr, tui.Warn.Render("  "+s))
		}
		if err != nil || len(targets) == 0 {
			return err
		}
	}

	fmt.Println(tui.Bold.Render("rollout restart"))
	for _, t := range targets {
		tag := ""
		if t.c.Prod {
			tag = " " + tui.Prod.Render(" PROD ")
		}
		fmt.Printf("  %-10s %s %s/%s%s\n", t.c.Short, kube.ShortKind(t.Kind), t.Namespace, t.Name, tag)
	}
	if prod := prodClusters(targets); len(prod) > 0 && os.Getenv("REX8S_FORCE") == "" &&
		!confirmProd(fmt.Sprintf("Restart %d workload(s) on", len(targets)), prod) {
		fmt.Fprintln(os.Stderr, "cancelled.")
		return nil
	}
	fmt.Println()

	states := make([]rollState, len(targets))
	for i, t := range targets {
		states[i] = rollState{t: t, start: time.Now()}
		if _, err := kubectlOut(k, a.src, 30*time.Second, "--context", t.c.Name, "-n", t.Namespace,
			"rollout", "restart", strings.ToLower(t.Kind)+"/"+t.Name); err != nil {
			states[i].err = lastLogLine(err.Error())
			states[i].end = time.Now()
		}
	}

	tty := term.IsTerminal(int(os.Stdout.Fd()))
	drawn := 0
	draw := func() {
		if tty && drawn > 0 {
			fmt.Printf("\033[%dA", drawn)
		}
		for _, s := range states {
			if tty {
				fmt.Print("\033[2K")
			}
			fmt.Println(s.line())
		}
		drawn = len(states)
	}
	for {
		var wg sync.WaitGroup
		for i := range states {
			if states[i].finished() {
				continue
			}
			wg.Add(1)
			go func(s *rollState) {
				defer wg.Done()
				s.poll(k, a.src)
			}(&states[i])
		}
		wg.Wait()

		done, failed := 0, 0
		for _, s := range states {
			if s.finished() {
				done++
				if s.err != "" {
					failed++
				}
			}
		}
		if tty {
			draw()
		}
		if done == len(states) {
			if !tty {
				draw()
			}
			if failed > 0 {
				msg := fmt.Sprintf("kroll: %d of %d failed", failed, len(states))
				rex.Status("", "error", "", msg)
				return fmt.Errorf("%d of %d rollouts failed", failed, len(states))
			}
			msg := fmt.Sprintf("kroll: %d rolled out", len(states))
			rex.Status("", "done", "", msg)
			fmt.Println()
			fmt.Println(tui.Ok.Render(fmt.Sprintf("%d rolled out", len(states))))
			return nil
		}
		rex.Status("", "working", "", fmt.Sprintf("kroll %d/%d rolled out", done, len(states)))
		time.Sleep(2 * time.Second)
	}
}

// rollState follows one workload's rollout after the restart.
type rollState struct {
	t          target
	w          kube.Workload
	seen       bool
	start, end time.Time
	err        string
}

func (s *rollState) finished() bool { return !s.end.IsZero() }

func (s *rollState) poll(k, kubeconfig string) {
	out, err := kubectlOut(k, kubeconfig, 20*time.Second, "--context", s.t.c.Name, "-n", s.t.Namespace,
		"get", strings.ToLower(s.t.Kind), s.t.Name, "-o", "json")
	if err == nil {
		var ws []kube.Workload
		if ws, err = kube.ParseWorkloads([]byte(`{"items":[` + out + `]}`)); err == nil && len(ws) == 1 {
			s.w, s.seen = ws[0], true
		}
	}
	switch {
	case s.seen && s.w.Failed:
		s.err, s.end = "rollout stuck: "+s.w.Message, time.Now()
	case s.seen && s.w.RolledOut():
		s.end = time.Now()
	case time.Since(s.start) > rollTimeout:
		s.err, s.end = "not rolled out after "+rollTimeout.String(), time.Now()
	}
}

func (s *rollState) line() string {
	name := fmt.Sprintf("%-10s %s %s/%s", s.t.c.Short, kube.ShortKind(s.t.Kind), s.t.Namespace, s.t.Name)
	took := func() string { return tui.Dim.Render(" " + s.end.Sub(s.start).Round(time.Second).String()) }
	switch {
	case s.err != "":
		return tui.Err.Render("  ✗ ") + name + "  " + tui.Err.Render(s.err)
	case s.finished():
		return tui.Ok.Render("  ✓ ") + name + "  " + tui.Ok.Render(fmt.Sprintf("%d/%d ready", s.w.Ready, s.w.Desired)) + took()
	case !s.seen:
		return tui.Dim.Render("  … ") + name
	}
	return tui.Warn.Render("  ⟳ ") + name + "  " +
		fmt.Sprintf("%d/%d updated · %d ready", s.w.Updated, s.w.Desired, s.w.Ready)
}
