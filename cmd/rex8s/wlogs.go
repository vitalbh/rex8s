package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// logSpec is one workload's pods to follow: "ctx|ns|selector|label".
type logSpec struct{ ctx, ns, selector, label string }

func (s logSpec) String() string {
	return strings.Join([]string{s.ctx, s.ns, s.selector, s.label}, "|")
}

func parseLogSpec(s string) (logSpec, bool) {
	p := strings.SplitN(s, "|", 4)
	if len(p) != 4 || p[0] == "" || p[2] == "" {
		return logSpec{}, false
	}
	return logSpec{p[0], p[1], p[2], p[3]}, true
}

// klogs: pick a workload (all its pods) or a single pod in this namespace and
// follow the logs in a split. klogs -A: workloads on any cluster (mark
// several), merged into one stream.
//
//	-g REGEX   only lines matching REGEX (case-insensitive), matches highlighted
//	-s DUR     start DUR back (e.g. 1h) instead of the last lines
func (a *app) klogs(args []string) error { return a.klogsWith(args, false) }

// kgrep: klogs that searches — kgrep [-A] [PATTERN]: the pattern is asked
// after the pick when not given.
func (a *app) kgrep(args []string) error {
	var rest []string
	pattern := ""
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-A" || a == "-s" || a == "--since":
			rest = append(rest, a)
			if a != "-A" && i+1 < len(args) {
				rest = append(rest, args[i+1])
				i++
			}
		case pattern == "":
			pattern = a
		default:
			rest = append(rest, a)
		}
	}
	if pattern != "" {
		rest = append(rest, "-g", pattern)
	}
	return a.klogsWith(rest, pattern == "")
}

// askPattern prompts for a regex on the terminal ("" cancels).
func askPattern() string {
	fmt.Fprint(os.Stderr, tui.Bold.Render("Search the logs for")+tui.Dim.Render(" (regex, case-insensitive; empty cancels)")+"\n  > ")
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return ""
	}
	defer tty.Close()
	line, _ := bufio.NewReader(tty).ReadString('\n')
	return strings.TrimSpace(line)
}

func (a *app) klogsWith(args []string, ask bool) error {
	filter, all := "", false
	var lo logOpts
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-A":
			all = true
		case (arg == "-g" || arg == "--grep") && i+1 < len(args):
			lo.grep = args[i+1]
			i++
		case (arg == "-s" || arg == "--since") && i+1 < len(args):
			lo.since = args[i+1]
			i++
		default:
			filter = arg
		}
	}
	if _, err := lo.compile(); err != nil {
		return err
	}
	if all {
		return a.klogsClusters(filter, lo, ask)
	}
	c, err := kube.Current(a.cfg, "", "")
	if err != nil {
		return err
	}
	ns := nsOr(c.Namespace)
	scope, where := []string{"-n", ns}, c.Short+"/"+ns
	if c.AllNS {
		scope, where = []string{"-A"}, c.Short+" (all namespaces)"
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	pods := map[string]pod{}
	works := map[string]target{}
	it, err := tui.Run(tui.Options{
		Title: fmt.Sprintf("Logs · %s · a workload follows all its pods", where),
		Query: filter,
		Load: func() ([]tui.Item, error) {
			var wout, pout []byte
			var werr, perr error
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				wout, werr = cl.Get(c.Name, append([]string{"get", "deployments,statefulsets,daemonsets", "-o", "json"}, scope...)...)
			}()
			go func() {
				defer wg.Done()
				pout, perr = cl.Get(c.Name, append([]string{"get", "pods", "-o", "json"}, scope...)...)
			}()
			wg.Wait()
			if perr != nil {
				return nil, perr
			}
			ps, err := parsePods(pout)
			if err != nil {
				return nil, err
			}
			var ws []target
			if werr == nil {
				ws, _ = parseWorkloadSelectors(wout)
			}
			// every namespace: names carry theirs
			wname := func(t target) string {
				if c.AllNS {
					return t.Namespace + "/" + t.Name
				}
				return t.Name
			}
			pname := func(p pod) string {
				if c.AllNS {
					return p.Namespace + "/" + p.Name
				}
				return p.Name
			}
			w := 0
			for _, t := range ws {
				w = max(w, len(kube.ShortKind(t.Kind))+1+len(wname(t)))
			}
			for _, p := range ps {
				w = max(w, len(pname(p)))
			}
			var items []tui.Item
			for _, t := range ws {
				if t.Selector == "" {
					continue
				}
				n := 0
				for _, p := range ps {
					if p.Namespace == t.Namespace && matchesSelector(p, t) {
						n++
					}
				}
				key := "w:" + t.Kind + "/" + t.Namespace + "/" + t.Name
				works[key] = t
				items = append(items, tui.Item{Key: key, Label: fmt.Sprintf("%-*s", w, kube.ShortKind(t.Kind)+" "+wname(t)),
					Hint: fmt.Sprintf("all %d pod(s), merged", n), Badge: "ALL"})
			}
			rank := map[string]int{"FAIL": 0, "WAIT": 1, "ok": 2, "done": 3}
			sort.SliceStable(ps, func(i, j int) bool { return rank[ps[i].badge()] < rank[ps[j].badge()] })
			for _, p := range ps {
				pods[pname(p)] = p
				items = append(items, tui.Item{Key: pname(p), Label: fmt.Sprintf("%-*s", w, pname(p)), Badge: p.badge(),
					Hint: fmt.Sprintf("%-18s %d/%d  restarts %-3d %s", p.status(), p.Ready, p.Total, p.Restarts, age(p.Created))})
			}
			if len(items) == 0 {
				return nil, fmt.Errorf("no pods in %s", where)
			}
			return items, nil
		},
	})
	if err != nil || it == nil {
		return err
	}
	if ask {
		if lo.grep = askPattern(); lo.grep == "" {
			return nil
		}
		if _, err := lo.compile(); err != nil {
			return err
		}
	}
	self := filepath.Join(a.binDir, "rex8s")
	if t, ok := works[it.Key]; ok {
		spec := logSpec{c.Name, t.Namespace, t.Selector, kube.ShortKind(t.Kind) + " " + t.Name}
		if rex.Block() == "" {
			return a.wlogtail([]string{spec.String()}, lo)
		}
		return rex.SplitBehind("logs "+t.Name, append([]string{self, "wlogtail"}, append(lo.flags(), spec.String())...)...)
	}
	p := pods[it.Key]
	container, ok, err := pickContainer(p, true)
	if err != nil || !ok {
		return err
	}
	if rex.Block() == "" {
		return a.logtail(c.Name, p.Namespace, p.Name, container, lo)
	}
	argv := append([]string{self, "logtail"}, lo.flags()...)
	argv = append(argv, c.Name, p.Namespace, p.Name)
	if container != "" {
		argv = append(argv, container)
	}
	return rex.SplitBehind("logs "+p.Name, argv...)
}

// matchesSelector: the pod carries the workload's matchLabels. The pod list
// has no labels, so this asks by name prefix (pods are named after their
// workload) — only used for the count shown in the picker.
func matchesSelector(p pod, t target) bool {
	return strings.HasPrefix(p.Name, t.Name+"-")
}

// klogsClusters: workloads on any cluster (krun's picker), logs merged.
func (a *app) klogsClusters(filter string, lo logOpts, ask bool) error {
	k, err := a.kubectl()
	if err != nil {
		return err
	}
	raw, err := kube.Load(a.src)
	if err != nil {
		return err
	}
	var skipped []string
	targets, _, err := a.pickTargets(k, uniqueClusters(kube.Contexts(raw, a.cfg)), filter, "Follow the logs of which workloads?", &skipped)
	for _, s := range skipped {
		fmt.Fprintln(os.Stderr, tui.Warn.Render("  "+s))
	}
	if err != nil || len(targets) == 0 {
		return err
	}
	if ask {
		if lo.grep = askPattern(); lo.grep == "" {
			return nil
		}
		if _, err := lo.compile(); err != nil {
			return err
		}
	}
	var specs []string
	for _, t := range targets {
		if t.Selector == "" {
			continue
		}
		specs = append(specs, logSpec{t.c.Name, t.Namespace, t.Selector, t.c.Short + " " + t.Name}.String())
	}
	if rex.Block() == "" {
		return a.wlogtail(specs, lo)
	}
	argv := append([]string{filepath.Join(a.binDir, "rex8s"), "wlogtail"}, lo.flags()...)
	return rex.SplitBehind("logs "+describeTargets(targets), append(argv, specs...)...)
}

var streamColors = []string{"39", "214", "170", "42", "203", "111"}

// wlogtail follows every pod of one or more workloads, one merged stream
// (runs in its own pane). When the set of pods changes (a rollout), the
// stream is restarted so the new pods show up.
func (a *app) wlogtail(raw []string, lo logOpts) error {
	re, err := lo.compile()
	if err != nil {
		return err
	}
	var specs []logSpec
	for _, r := range raw {
		if s, ok := parseLogSpec(r); ok {
			specs = append(specs, s)
		}
	}
	if len(specs) == 0 {
		return fmt.Errorf("wlogtail: nothing to follow")
	}
	k, err := a.kubectl()
	if err != nil {
		return err
	}
	var labels []string
	prod := false
	for _, s := range specs {
		labels = append(labels, s.label)
		if c, err := a.context(s.ctx); err == nil && c.Prod {
			prod = true
		}
	}
	rex.Rename("logs " + strings.Join(labels, ", "))
	if c, err := a.context(specs[0].ctx); err == nil {
		if prod {
			c.Prod = true
		}
		a.tint(c)
	}
	fmt.Println(tui.Dim.Render("following " + strings.Join(labels, " · ") + lo.describe() + " · new pods join after a rollout · ctrl+c to stop"))

	var out sync.Mutex
	var wg sync.WaitGroup
	for i, s := range specs {
		prefix := ""
		if len(specs) > 1 {
			prefix = lipgloss.NewStyle().Foreground(lipgloss.Color(streamColors[i%len(streamColors)])).Render(s.label) + " "
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.followWorkload(k, s, prefix, &out, lo.since, re)
		}()
	}
	wg.Wait()
	fmt.Fprint(os.Stderr, tui.Dim.Render("\nlogs ended · press enter to close"))
	bufio.NewReader(os.Stdin).ReadString('\n')
	return nil
}

func (a *app) followWorkload(k string, s logSpec, prefix string, out *sync.Mutex, since string, re *regexp.Regexp) {
	for {
		names, err := runningPods(k, a.src, s.ctx, s.ns, s.selector)
		if err != nil || len(names) == 0 {
			out.Lock()
			fmt.Println(prefix + tui.Dim.Render("(no running pods; retrying)"))
			out.Unlock()
			time.Sleep(10 * time.Second)
			continue
		}
		args := []string{"--context", s.ctx, "-n", s.ns, "logs", "-f", "-l", s.selector, "--prefix",
			"--all-containers", "--max-log-requests=50"}
		if since == "" {
			tail := "--tail=30"
			if re != nil {
				tail = "--tail=500" // grep: look further back
			}
			args = append(args, tail)
		} else {
			args = append(args, "--since="+since)
		}
		cmd := exec.Command(k, args...)
		cmd.Env = setEnv(os.Environ(), "KUBECONFIG", a.src)
		pipe, _ := cmd.StdoutPipe()
		cmd.Stderr = cmd.Stdout
		if err := cmd.Start(); err != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			copyLines(pipe, prefix, out, re)
			close(done)
		}()
		// restart when the pods change, so new ones are followed too
		changed := false
		start := time.Now()
	watch:
		for {
			select {
			case <-done:
				break watch
			case <-time.After(15 * time.Second):
				now, err := runningPods(k, a.src, s.ctx, s.ns, s.selector)
				if err == nil && strings.Join(now, ",") != strings.Join(names, ",") {
					changed = true
					_ = cmd.Process.Kill()
					<-done
					break watch
				}
			}
		}
		_ = cmd.Wait()
		if !changed && time.Since(start) < 5*time.Second {
			time.Sleep(5 * time.Second) // kubectl failing fast: don't spin
		}
		if changed {
			out.Lock()
			fmt.Println(prefix + tui.Dim.Render("── pods changed, following the new set ──"))
			out.Unlock()
		}
		since = "30s" // the new pods' first lines (a few may repeat)
	}
}

func copyLines(r io.Reader, prefix string, out *sync.Mutex, re *regexp.Regexp) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line, ok := grepLine(sc.Text(), re)
		if !ok {
			continue
		}
		out.Lock()
		fmt.Println(prefix + line)
		out.Unlock()
	}
}

// logOpts: klogs -g / -s, passed to the log panes as --grep=/--since=.
type logOpts struct{ grep, since string }

func (lo logOpts) flags() []string {
	var f []string
	if lo.grep != "" {
		f = append(f, "--grep="+lo.grep)
	}
	if lo.since != "" {
		f = append(f, "--since="+lo.since)
	}
	return f
}

func (lo logOpts) compile() (*regexp.Regexp, error) {
	if lo.grep == "" {
		return nil, nil
	}
	re, err := regexp.Compile("(?i)" + lo.grep)
	if err != nil {
		return nil, fmt.Errorf("-g %q: %v", lo.grep, err)
	}
	return re, nil
}

func (lo logOpts) describe() string {
	s := ""
	if lo.grep != "" {
		s += " · only /" + lo.grep + "/"
	}
	if lo.since != "" {
		s += " · since " + lo.since
	}
	return s
}

// splitLogFlags takes --grep=/--since= out of a log pane's arguments.
func splitLogFlags(args []string) ([]string, logOpts) {
	var lo logOpts
	var rest []string
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--grep="):
			lo.grep = strings.TrimPrefix(a, "--grep=")
		case strings.HasPrefix(a, "--since="):
			lo.since = strings.TrimPrefix(a, "--since=")
		default:
			rest = append(rest, a)
		}
	}
	return rest, lo
}

var matchStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("214"))

// grepLine: with no pattern every line passes; otherwise only matching
// lines, with the matches highlighted.
func grepLine(line string, re *regexp.Regexp) (string, bool) {
	if re == nil {
		return line, true
	}
	if !re.MatchString(line) {
		return "", false
	}
	return re.ReplaceAllStringFunc(line, func(m string) string { return matchStyle.Render(m) }), true
}
