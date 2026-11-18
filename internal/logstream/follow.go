package logstream

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// Spec is one workload's pods to follow — or one pod, when Selector starts
// with PodSelector. Its text form is "ctx|ns|selector|label"; Label is
// "CLUSTER NAME".
type Spec struct{ Ctx, NS, Selector, Label string }

func (s Spec) String() string {
	return strings.Join([]string{s.Ctx, s.NS, s.Selector, s.Label}, "|")
}

// ParseSpec reads a Spec's text form.
func ParseSpec(s string) (Spec, bool) {
	p := strings.SplitN(s, "|", 4)
	if len(p) != 4 || p[0] == "" || p[2] == "" {
		return Spec{}, false
	}
	return Spec{p[0], p[1], p[2], p[3]}, true
}

// PodSelector marks a Spec that follows one pod instead of a workload:
// "pod:NAME".
const PodSelector = "pod:"

// StreamColors are the colors of the sources of a merged stream, in turn.
var StreamColors = []string{"39", "214", "170", "42", "203", "111"}

// StreamLabel: for a stream of several sources "CLUSTER POD" in the
// source's color, for one source the pod alone, dim; the container only
// when it is not named like the workload or the pod (web-7d9f/web). label
// is the Spec's ("CLUSTER NAME").
func StreamLabel(label, color string, multi bool) Label {
	short, name, _ := strings.Cut(label, " ")
	style := tui.Dim
	if multi {
		style = lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	}
	return func(pod, container string) string {
		text := label
		if pod != "" {
			text = pod
			if multi {
				text = short + " " + pod
			}
			if container != "" && container != name && !strings.HasPrefix(pod, container) {
				text += "/" + container
			}
		} else if !multi {
			return ""
		}
		return style.Render(text) + " "
	}
}

// Follower prints log streams, one line at a time, to Out. One Follower
// serves every stream of a pane: its lock keeps their lines whole.
type Follower struct {
	Client kube.Client
	// Clusters by context name: the short name in the kauth hint. A
	// context missing here goes by its name.
	Clusters map[string]kube.Context
	// Links makes the Linker of a stream's cluster and namespace; nil, or
	// returning nil, means no links.
	Links func(ctx, ns string) Linker
	Out   io.Writer      // nil: os.Stdout
	Grep  *regexp.Regexp // only matching lines, matches highlighted
	Since string         // start this far back (kubectl --since) instead of the last lines

	// Poll is how often a workload's pods are checked for a change (15s),
	// Retry how long to wait after a failure (10s).
	Poll, Retry time.Duration

	mu sync.Mutex
}

func (f *Follower) println(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.Out
	if out == nil {
		out = os.Stdout
	}
	fmt.Fprintln(out, s)
}

// say prints a dim status line led by the stream's label.
func (f *Follower) say(label Label, msg string) {
	lead := ""
	if label != nil {
		lead = label("", "")
	}
	f.println(lead + tui.Dim.Render(msg))
}

func (f *Follower) poll() time.Duration  { return cmpOr(f.Poll, 15*time.Second) }
func (f *Follower) retry() time.Duration { return cmpOr(f.Retry, 10*time.Second) }

func cmpOr(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (f *Follower) linker(s Spec) Linker {
	if f.Links == nil {
		return nil
	}
	return f.Links(s.Ctx, s.NS)
}

// short is the cluster's short name, for the kauth hint.
func (f *Follower) short(ctx string) string {
	if c, ok := f.Clusters[ctx]; ok && c.Short != "" {
		return c.Short
	}
	return ctx
}

// why says why the pods could not be listed: a login that expired points
// at kauth.
func (f *Follower) why(s Spec, err error) string {
	if kube.AuthExpired(err) {
		return "login expired · run kauth " + f.short(s.Ctx)
	}
	return "cannot list pods: " + tui.Clean(err.Error())
}

// Follow follows s until ctx ends: a pod until it is gone, a workload's
// pods for good (a rollout restarts the stream so the new pods show up;
// failures are said once and retried).
func (f *Follower) Follow(ctx context.Context, s Spec, label Label) {
	if strings.HasPrefix(s.Selector, PodSelector) {
		f.followPod(ctx, s, label)
		return
	}
	f.followWorkload(ctx, s, label)
}

// start runs kubectl with stdout and stderr on one pipe. kubectl stays in
// the terminal's process group, so ctrl+c stops it with the pane.
func (f *Follower) start(s Spec, args []string) (*exec.Cmd, io.Reader, error) {
	cmd := f.Client.Command(context.Background(), s.Ctx, args...)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, pipe, nil
}

func (f *Follower) followPod(ctx context.Context, s Spec, label Label) {
	name := strings.TrimPrefix(s.Selector, PodSelector)
	args := []string{"-n", s.NS, "logs", "-f", name, "--all-containers", "--prefix"}
	switch {
	case f.Since != "":
		args = append(args, "--since="+f.Since)
	case f.Grep != nil:
		args = append(args, "--tail=500")
	default:
		args = append(args, "--tail=30")
	}
	cmd, pipe, err := f.start(s, args)
	if err != nil {
		f.say(label, "(cannot start kubectl for pod "+name+": "+err.Error()+")")
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = f.Copy(pipe, f.linker(s), label, "")
	}()
	select {
	case <-done:
	case <-ctx.Done():
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	<-done
	if ctx.Err() == nil {
		f.say(label, "(logs of pod "+name+" ended)")
	}
}

// MaxLogRequests is kubectl's --max-log-requests for following every
// container of pods (init containers included: one per pod is allowed
// for), never below 50.
func MaxLogRequests(pods []kube.Pod) int {
	n := 0
	for _, p := range pods {
		n += max(1, len(p.Containers)) + 1
	}
	return max(50, n)
}

func podNames(ps []kube.Pod) string {
	names := make([]string, len(ps))
	for i, p := range ps {
		names[i] = p.Name
	}
	return strings.Join(names, ",")
}

func (f *Follower) followWorkload(ctx context.Context, s Spec, label Label) {
	links := f.linker(s)
	since := f.Since
	said := "" // the last status said, so a failure that repeats is said once
	tell := func(msg string) {
		if msg != said {
			said = msg
			f.say(label, msg)
		}
	}
	for ctx.Err() == nil {
		pods, err := f.Client.Running(s.Ctx, s.NS, s.Selector)
		if err != nil || len(pods) == 0 {
			if err != nil {
				tell("(" + f.why(s, err) + "; retrying)")
			} else {
				tell("(no running pods; retrying)")
			}
			sleep(ctx, f.retry())
			continue
		}
		args := []string{"-n", s.NS, "logs", "-f", "-l", s.Selector, "--prefix",
			"--all-containers", "--max-log-requests=" + strconv.Itoa(MaxLogRequests(pods))}
		switch {
		case since != "":
			args = append(args, "--since="+since)
		case f.Grep != nil:
			args = append(args, "--tail=500") // grep: look further back
		default:
			args = append(args, "--tail=30")
		}
		cmd, pipe, err := f.start(s, args)
		if err != nil {
			tell("(cannot start kubectl: " + err.Error() + "; retrying)")
			sleep(ctx, f.retry())
			continue
		}
		said = ""
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = f.Copy(pipe, links, label, "")
		}()
		// restart when the pods change, so new ones are followed too
		changed := false
		started := time.Now()
		tick := time.NewTicker(f.poll())
	watch:
		for {
			select {
			case <-done:
				break watch
			case <-ctx.Done():
				_ = cmd.Process.Kill()
				break watch
			case <-tick.C:
				now, err := f.Client.Running(s.Ctx, s.NS, s.Selector)
				if err == nil && podNames(now) != podNames(pods) {
					changed = true
					_ = cmd.Process.Kill()
					break watch
				}
			}
		}
		tick.Stop()
		// Wait also closes the pipe: Copy ends even if something kubectl
		// started still holds the other end
		_ = cmd.Wait()
		<-done
		if ctx.Err() != nil {
			return
		}
		if changed {
			if pr, ok := links.(Pruner); ok && pr != nil {
				alive := map[string]bool{}
				if now, err := f.Client.Running(s.Ctx, s.NS, s.Selector); err == nil {
					for _, p := range now {
						alive[p.Name] = true
					}
					pr.Prune(func(pod string) bool { return alive[pod] })
				}
			}
			f.say(label, "── pods changed, following the new set ──")
		} else if time.Since(started) < f.retry()/2 {
			sleep(ctx, f.retry()/2) // kubectl failing fast: don't spin
		}
		since = "30s" // the new pods' first lines (a few may repeat)
	}
}

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
