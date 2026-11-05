package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// fwatch follows the current git branch until Flux has applied it.
//
//	fwatch            the current pane's cluster
//	fwatch -A         every visible cluster (aliases of the same server deduplicated)
//	fwatch --now      also ask Flux to fetch the source right away
//
// It works with forks and pull requests: a Flux revision counts as "containing"
// the branch when it is HEAD, descends from HEAD (merge commit), or has the
// same content for every file the branch changed (squash/rebase merges).
func (a *app) fwatch(args []string) error {
	fs := flag.NewFlagSet("fwatch", flag.ContinueOnError)
	all := fs.Bool("A", false, "watch every cluster")
	now := fs.Bool("now", false, "request an immediate reconcile of the GitRepository")
	timeout := fs.Duration("timeout", 30*time.Minute, "give up after this long")
	interval := fs.Duration("interval", 5*time.Second, "poll interval")
	if err := fs.Parse(args); err != nil {
		return err
	}

	g, err := newGitTracker()
	if err != nil {
		return err
	}
	targets, err := a.watchTargets(*all)
	if err != nil {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}

	short := g.head[:7]
	fmt.Printf("%s %s %s\n", tui.Bold.Render("fwatch"), tui.Ok.Render(short), tui.Dim.Render(g.subject))
	updates := make(chan watchUpdate)
	for _, c := range targets {
		go watchCluster(cl, c, g, *now, *interval, *timeout, updates)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sig)

	start := time.Now()
	last := map[string]watchUpdate{}
	pending := len(targets)
	for pending > 0 {
		select {
		case <-sig:
			rex.Status("", "idle", "", "")
			fmt.Println(tui.Dim.Render("\ninterrupted."))
			return nil
		case u := <-updates:
			if u.final() {
				pending--
			}
			if prev, ok := last[u.cluster]; ok && prev.line() == u.line() {
				continue
			}
			last[u.cluster] = u
			fmt.Printf("  %-8s %s\n", u.cluster, u.render(time.Since(start)))
			rex.Status("", "working", "", "fwatch "+short+": "+summary(last))
		}
	}

	var ok, failed []string
	for name, u := range last {
		switch u.phase {
		case phaseDone:
			ok = append(ok, name)
		case phaseFailed:
			failed = append(failed, name)
		}
	}
	sort.Strings(ok)
	sort.Strings(failed)
	took := time.Since(start).Round(time.Second)
	switch {
	case len(failed) > 0:
		msg := short + " failed on " + strings.Join(failed, ", ")
		if len(ok) > 0 {
			msg += "; applied on " + strings.Join(ok, ", ")
		}
		rex.Status("", "error", "", msg)
		rex.Notify("fwatch", msg)
		fmt.Println(tui.Err.Render(msg))
	case len(ok) > 0:
		msg := short + " applied on " + strings.Join(ok, ", ") + " (" + took.String() + ")"
		rex.Status("", "done", "", msg)
		rex.Notify("fwatch", msg)
		fmt.Println(tui.Ok.Render(msg))
	default:
		msg := "no Flux GitRepository tracks any remote of this repo"
		rex.Status("", "error", "", msg)
		fmt.Println(tui.Warn.Render(msg))
	}
	return nil
}

// watchTargets returns the current context, or every visible one deduplicated
// by server URL.
func (a *app) watchTargets(all bool) ([]kube.Context, error) {
	if !all {
		c, err := kube.Current(a.cfg, "", "")
		if err != nil {
			return nil, err
		}
		return []kube.Context{c}, nil
	}
	raw, err := kube.Load(a.src)
	if err != nil {
		return nil, err
	}
	return dedupe(kube.Contexts(raw, a.cfg)), nil
}

// dedupe keeps one context per server (the first, contexts are sorted).
func dedupe(ctxs []kube.Context) []kube.Context {
	seen := map[string]bool{}
	var out []kube.Context
	for _, c := range ctxs {
		key := c.Server
		if key == "" {
			key = c.Name
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// ── git side ────────────────────────────────────────────────────────────

// gitTracker answers "does this Flux revision contain the local branch?".
type gitTracker struct {
	head, subject string
	remotes       map[string]string // normalized URL → remote name

	mu      sync.Mutex
	paths   map[string][]string // "remote/branch" → files the branch changed vs it
	results map[string]bool     // remote|branch|sha → contains
	fetched map[string]time.Time
	lag     map[string]int // "remote/branch" → commits HEAD is behind
}

func git(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

func newGitTracker() (*gitTracker, error) {
	head, err := git("rev-parse", "HEAD")
	if err != nil {
		return nil, errors.New("not inside a git repository")
	}
	subject, _ := git("log", "-1", "--format=%s")
	names, _ := git("remote")
	g := &gitTracker{head: head, subject: subject, remotes: map[string]string{},
		paths: map[string][]string{}, results: map[string]bool{}, fetched: map[string]time.Time{}, lag: map[string]int{}}
	for _, n := range strings.Fields(names) {
		if u, err := git("remote", "get-url", n); err == nil {
			g.remotes[kube.NormalizeGitURL(u)] = n
		}
	}
	if len(g.remotes) == 0 {
		return nil, errors.New("this repository has no git remotes")
	}
	return g, nil
}

// behind fetches the branch repository r tracks and returns how many of its
// commits HEAD is missing, with the ref name ("upstream/main"). Cached.
func (g *gitTracker) behind(r kube.GitRepo) (n int, ref string) {
	remote := g.remotes[kube.NormalizeGitURL(r.URL)]
	branch := r.Branch
	if branch == "" {
		branch = "main"
	}
	ref = remote + "/" + branch
	g.mu.Lock()
	defer g.mu.Unlock()
	if v, ok := g.lag[ref]; ok {
		return v, ref
	}
	_, _ = git("fetch", "-q", remote, branch)
	g.fetched[ref] = time.Now()
	out, err := git("rev-list", "--count", "HEAD.."+ref)
	if err == nil {
		n, _ = strconv.Atoi(out)
	}
	g.lag[ref] = n
	return n, ref
}

// tracks reports whether the GitRepository points at one of our remotes.
func (g *gitTracker) tracks(r kube.GitRepo) bool {
	_, ok := g.remotes[kube.NormalizeGitURL(r.URL)]
	return ok
}

// contains reports whether rev (a Flux revision of repository r) includes HEAD.
func (g *gitTracker) contains(r kube.GitRepo, rev string) bool {
	sha := kube.RevisionSHA(rev)
	if sha == "" {
		return false
	}
	if kube.HasRevision(rev, g.head) {
		return true
	}
	remote := g.remotes[kube.NormalizeGitURL(r.URL)]
	branch := r.Branch
	if branch == "" {
		branch = "main"
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := remote + "|" + branch + "|" + sha
	if v, ok := g.results[key]; ok {
		return v
	}
	if _, err := git("cat-file", "-e", sha+"^{commit}"); err != nil {
		// unknown commit: fetch the branch (at most every 10s)
		fk := remote + "/" + branch
		if time.Since(g.fetched[fk]) < 10*time.Second {
			return false
		}
		g.fetched[fk] = time.Now()
		if _, err := git("fetch", "-q", remote, branch); err != nil {
			return false
		}
		if _, err := git("cat-file", "-e", sha+"^{commit}"); err != nil {
			return false
		}
	}
	v := g.check(remote, branch, sha)
	g.results[key] = v
	return v
}

func (g *gitTracker) check(remote, branch, sha string) bool {
	// merge commit / fast-forward: HEAD is an ancestor of the Flux revision
	if _, err := git("merge-base", "--is-ancestor", g.head, sha); err == nil {
		return true
	}
	// squash or rebase: every file the branch changed matches the Flux revision
	pk := remote + "/" + branch
	paths, ok := g.paths[pk]
	if !ok {
		if base, err := git("merge-base", g.head, sha); err == nil {
			if out, err := git("diff", "--name-only", base, g.head); err == nil && out != "" {
				paths = strings.Split(out, "\n")
			}
		}
		g.paths[pk] = paths
	}
	if len(paths) == 0 {
		return false
	}
	args := append([]string{"diff", "--quiet", g.head, sha, "--"}, paths...)
	_, err := git(args...)
	return err == nil
}

// ── per-cluster state ───────────────────────────────────────────────────

type watchPhase int

const (
	phaseSource    watchPhase = iota // waiting for the GitRepository to fetch the change
	phaseApplying                    // source has it; Kustomizations still applying
	phaseDone                        // every Kustomization of the source applied it
	phaseFailed                      // a Kustomization failed with it, or no access
	phaseUntracked                   // no GitRepository for this repo
	phaseRollout                     // applied; waiting for the workloads it changed to roll out
)

type watchUpdate struct {
	cluster        string
	phase          watchPhase
	applied, total int
	detail         string

	rolled, rolling int             // workloads rolled out / being tracked
	kustomizations  map[string]bool // "ns/name" of the Kustomizations fed by the repo
}

func (u watchUpdate) final() bool {
	return u.phase == phaseDone || u.phase == phaseFailed || u.phase == phaseUntracked
}

func (u watchUpdate) line() string {
	return fmt.Sprintf("%d %d/%d %d/%d %s", u.phase, u.applied, u.total, u.rolled, u.rolling, u.detail)
}

func (u watchUpdate) render(elapsed time.Duration) string {
	t := tui.Dim.Render(fmt.Sprintf("  %s", elapsed.Round(time.Second)))
	switch u.phase {
	case phaseSource:
		return tui.Warn.Render("waiting for Flux to fetch it") + tui.Dim.Render(" "+u.detail) + t
	case phaseApplying:
		return tui.Warn.Render(fmt.Sprintf("applying %d/%d", u.applied, u.total)) + tui.Dim.Render(" "+u.detail) + t
	case phaseRollout:
		return tui.Warn.Render(fmt.Sprintf("rolling out %d/%d", u.rolled, u.rolling)) + tui.Dim.Render(" "+u.detail) + t
	case phaseDone:
		msg := fmt.Sprintf("applied %d/%d", u.applied, u.total)
		if u.rolling > 0 {
			msg += fmt.Sprintf(" · rolled out %d", u.rolling)
		}
		return tui.Ok.Render(msg) + t
	case phaseFailed:
		return tui.Err.Render("failed: "+u.detail) + t
	}
	return tui.Dim.Render("not tracking this repo")
}

func summary(m map[string]watchUpdate) string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		u := m[n]
		switch u.phase {
		case phaseSource:
			parts = append(parts, n+" source")
		case phaseApplying:
			parts = append(parts, fmt.Sprintf("%s %d/%d", n, u.applied, u.total))
		case phaseRollout:
			parts = append(parts, fmt.Sprintf("%s rollout %d/%d", n, u.rolled, u.rolling))
		case phaseDone:
			parts = append(parts, n+" ok")
		case phaseFailed:
			parts = append(parts, n+" FAILED")
		}
	}
	return strings.Join(parts, ", ")
}

// tracker is what evaluate needs from git (an interface for tests).
type tracker interface {
	tracks(kube.GitRepo) bool
	contains(r kube.GitRepo, rev string) bool
}

// evaluate decides where the change stands on one cluster.
func evaluate(repos []kube.GitRepo, items []kube.FluxItem, g tracker) watchUpdate {
	tracked := map[string]kube.GitRepo{}
	fetched := false
	var sourceErr string
	for _, r := range repos {
		if !g.tracks(r) {
			continue
		}
		tracked[r.Namespace+"/"+r.Name] = r
		if g.contains(r, r.Revision) {
			fetched = true
		} else if r.Ready == "False" {
			sourceErr = r.Namespace + "/" + r.Name + ": " + oneLine(r.Message)
		}
	}
	if len(tracked) == 0 {
		return watchUpdate{phase: phaseUntracked}
	}
	if !fetched {
		if sourceErr != "" {
			return watchUpdate{phase: phaseFailed, detail: truncate(sourceErr, 160)}
		}
		return watchUpdate{phase: phaseSource}
	}

	u := watchUpdate{phase: phaseApplying, kustomizations: map[string]bool{}}
	var waiting []string
	for _, it := range items {
		if it.Kind != "Kustomization" || it.Suspended || it.SourceKind != "GitRepository" {
			continue
		}
		r, ok := tracked[it.SourceNamespace+"/"+it.SourceName]
		if !ok {
			continue
		}
		u.total++
		u.kustomizations[it.Namespace+"/"+it.Name] = true
		switch {
		case g.contains(r, it.AppliedRevision) && it.Ready == "True":
			u.applied++
		case g.contains(r, it.AttemptedRevision) && it.Ready == "False":
			return watchUpdate{phase: phaseFailed, applied: u.applied, total: u.total,
				detail: truncate(it.Namespace+"/"+it.Name+": "+oneLine(it.Message), 160)}
		default:
			waiting = append(waiting, it.Name)
		}
	}
	if u.applied == u.total {
		u.phase = phaseDone
		return u
	}
	if len(waiting) <= 3 {
		u.detail = strings.Join(waiting, " ")
	}
	return u
}

// evaluateRollout follows the workloads (and HelmReleases) applied by the
// repo's Kustomizations that this change touched: their generation moved past
// the baseline taken when fwatch started, they are new, or they are mid-rollout.
// Workloads that were already fully updated but unhealthy are ignored — the
// change didn't cause them.
// baselineEntry is a workload's state when fwatch started.
type baselineEntry struct {
	generation int64
	failed     bool // already stuck (e.g. ProgressDeadlineExceeded) before this change
}

func evaluateRollout(ws []kube.Workload, baseline map[string]baselineEntry, ks map[string]bool) watchUpdate {
	u := watchUpdate{phase: phaseRollout}
	var waiting []string
	for _, w := range ws {
		if !ks[w.KsNamespace+"/"+w.KsName] {
			continue
		}
		base, seen := baseline[w.Key()]
		if seen && w.Generation <= base.generation && (!w.InProgress() || base.failed) {
			continue // untouched by this change (healthy, or already stuck before it)
		}
		u.rolling++
		switch {
		case w.Failed:
			return watchUpdate{phase: phaseFailed, rolled: u.rolled, rolling: u.rolling,
				detail: truncate(kube.ShortKind(w.Kind)+"/"+w.Namespace+"/"+w.Name+": "+oneLine(w.Message), 160)}
		case w.RolledOut():
			u.rolled++
		default:
			waiting = append(waiting, fmt.Sprintf("%s/%s %d/%d", kube.ShortKind(w.Kind), w.Name, w.Ready, w.Desired))
		}
	}
	if u.rolled == u.rolling {
		u.phase = phaseDone
		return u
	}
	if len(waiting) <= 3 {
		u.detail = strings.Join(waiting, " ")
	} else {
		u.detail = strings.Join(waiting[:3], " ") + fmt.Sprintf(" +%d", len(waiting)-3)
	}
	return u
}

// rolloutState fetches workloads plus HelmReleases as one list.
func rolloutState(cl kube.Client, ctxName string, items []kube.FluxItem) ([]kube.Workload, error) {
	ws, err := cl.Workloads(ctxName)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.Kind == "HelmRelease" {
			ws = append(ws, it.AsWorkload())
		}
	}
	return ws, nil
}

func watchCluster(cl kube.Client, c kube.Context, g *gitTracker, now bool, interval, timeout time.Duration, out chan<- watchUpdate) {
	send := func(u watchUpdate) { u.cluster = c.Short; out <- u }
	deadline := time.Now().Add(timeout)
	requested := !now
	// baseline generations, to tell what this change touched (nil: rollout tracking off)
	var baseline map[string]baselineEntry
	if items, _, err := cl.FluxItems(c.Name); err == nil {
		if ws, err := rolloutState(cl, c.Name, items); err == nil {
			baseline = map[string]baselineEntry{}
			for _, w := range ws {
				baseline[w.Key()] = baselineEntry{generation: w.Generation, failed: w.Failed}
			}
		}
	}
	for {
		repos, err := cl.GitRepos(c.Name)
		var items []kube.FluxItem
		if err == nil {
			items, _, err = cl.FluxItems(c.Name)
		}
		if err != nil {
			if strings.Contains(err.Error(), "doesn't have a resource type") {
				send(watchUpdate{phase: phaseUntracked})
			} else {
				detail := "no access: " + err.Error()
				if kube.AuthExpired(err) {
					detail = "login expired · run kauth " + c.Short
				}
				send(watchUpdate{phase: phaseFailed, detail: detail})
			}
			return
		}
		u := evaluate(repos, items, g)
		if u.phase == phaseDone && baseline != nil { // applied: now wait for the rollout
			applied, total := u.applied, u.total
			if ws, err := rolloutState(cl, c.Name, items); err == nil {
				u = evaluateRollout(ws, baseline, u.kustomizations)
				u.applied, u.total = applied, total
			}
		}
		if !requested && u.phase == phaseSource {
			requested = true
			for _, r := range repos {
				if g.tracks(r) {
					_, _ = cl.Get(c.Name, "annotate", "--overwrite", "-n", r.Namespace, kube.GitRepositoryRes, r.Name,
						"reconcile.fluxcd.io/requestedAt="+time.Now().Format(time.RFC3339Nano))
				}
			}
		}
		if !u.final() && time.Now().After(deadline) {
			why := "timed out (merged into the branch Flux tracks?)"
			if u.phase == phaseRollout {
				why = "timed out waiting for the rollout: " + u.detail
			}
			u = watchUpdate{phase: phaseFailed, applied: u.applied, total: u.total, detail: why}
		}
		send(u)
		if u.final() {
			return
		}
		time.Sleep(interval)
	}
}
