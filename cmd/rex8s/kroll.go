package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/vitalbh/rex8s/internal/fleet"
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
			given = append(given, target{c: c, Workload: kube.Workload{Kind: p[1], Namespace: p[2], Name: p[3]}})
			continue
		}
		filter = args[i]
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	cl.Timeout = 30 * time.Second
	targets := given
	if len(targets) == 0 {
		raw, err := kube.Load(a.src)
		if err != nil {
			return err
		}
		clusters := fleet.All(raw, a.cfg)
		var skipped []string
		targets, _, err = a.pickTargets(cl, clusters, filter, "Restart which workloads?", &skipped)
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
	touched := make([]kube.Context, 0, len(targets))
	for _, t := range targets {
		touched = append(touched, t.c)
	}
	if ok, err := confirmChange(touched, fmt.Sprintf("kroll: rollout restart %d workload(s)", len(targets))); !ok {
		return err
	}
	fmt.Println()

	states := make([]rollState, len(targets))
	for i, t := range targets {
		states[i] = rollState{t: t, start: time.Now()}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := cl.Output(ctx, t.c.Name, "-n", t.Namespace, "rollout", "restart", strings.ToLower(t.Kind)+"/"+t.Name)
		cancel()
		if err != nil {
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
				s.poll(cl)
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

func (s *rollState) poll(cl kube.Client) {
	cl.Timeout = 20 * time.Second
	out, err := cl.Get(s.t.c.Name, "-n", s.t.Namespace, "get", strings.ToLower(s.t.Kind), s.t.Name, "-o", "json")
	if err == nil {
		var ws []kube.Workload
		if ws, err = kube.ParseWorkloads([]byte(`{"items":[` + string(out) + `]}`)); err == nil && len(ws) == 1 {
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
		return tui.Ok.Render("  ✓ ") + name + "  " + tui.Ok.Render(fmt.Sprintf("%d/%d ready", s.w.Available, s.w.Desired)) + took()
	case !s.seen:
		return tui.Dim.Render("  … ") + name
	}
	return tui.Warn.Render("  ⟳ ") + name + "  " +
		fmt.Sprintf("%d/%d updated · %d ready", s.w.Updated, s.w.Desired, s.w.Available)
}
