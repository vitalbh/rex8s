package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/pathx"
	"github.com/vitalbh/rex8s/internal/tui"
)

// fdiff previews what the local working tree would change on the cluster:
// `flux diff kustomization --path <local dir>` for every Kustomization whose
// GitRepository points at one of this repo's remotes. Uncommitted changes
// count, so it works before you even commit.
//
//	fdiff [-A] [FILTER]   FILTER keeps Kustomizations whose name contains it
func (a *app) fdiff(args []string) error {
	fs := flag.NewFlagSet("fdiff", flag.ContinueOnError)
	all := fs.Bool("A", false, "every cluster")
	if err := fs.Parse(args); err != nil {
		return err
	}
	filter := fs.Arg(0)

	fluxBin := pathx.Real("flux", a.binDir)
	if fluxBin == "" {
		return errors.New("flux CLI not found in PATH (brew install fluxcd/tap/flux)")
	}
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil {
		return errors.New("not inside a git repository")
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

	fmt.Println(tui.Bold.Render("fdiff") + tui.Dim.Render("  "+root+"  (working tree, uncommitted changes included)"))
	var mu sync.Mutex // one result block at a time on stdout
	print := func(s string) { mu.Lock(); fmt.Print(s); mu.Unlock() }
	warned := map[string]bool{}
	warnBehind := func(r kube.GitRepo) {
		n, ref := g.behind(r)
		mu.Lock()
		defer mu.Unlock()
		if n > 0 && !warned[ref] {
			warned[ref] = true
			fmt.Println(tui.Warn.Render(fmt.Sprintf("warning: your branch is %d commits behind %s — the diff shows them as reverted. Rebase or merge %s first.", n, ref, ref)))
		}
	}
	var wg sync.WaitGroup
	for _, c := range targets {
		wg.Add(1)
		go func(c kube.Context) {
			defer wg.Done()
			a.diffCluster(cl, fluxBin, root, g, c, filter, print, warnBehind)
		}(c)
	}
	wg.Wait()
	return nil
}

// diffCluster lists the cluster's Kustomizations from this repo and diffs them
// in parallel (after the listing call has refreshed the login, so concurrent
// calls don't race to refresh an OIDC token), printing each result as it lands.
func (a *app) diffCluster(cl kube.Client, fluxBin, root string, g *gitTracker, c kube.Context, filter string, print func(string), warnBehind func(kube.GitRepo)) {
	tag := tui.Ok.Render("dev ")
	if c.Prod {
		tag = tui.Prod.Render("PROD")
	}
	prefix := tag + " " + tui.Bold.Render(fmt.Sprintf("%-7s", c.Short)) + " "

	repos, err := cl.GitRepos(c.Name)
	var items []kube.FluxItem
	if err == nil {
		items, _, err = cl.FluxItems(c.Name)
	}
	if err != nil {
		print(prefix + accessError(c, err) + "\n")
		return
	}
	tracked := map[string]bool{}
	for _, r := range repos {
		if g.tracks(r) {
			tracked[r.Namespace+"/"+r.Name] = true
			warnBehind(r)
		}
	}
	var todo []kube.FluxItem
	for _, it := range items {
		if it.Kind != "Kustomization" || it.SourceKind != "GitRepository" || !tracked[it.SourceNamespace+"/"+it.SourceName] {
			continue
		}
		if filter != "" && !strings.Contains(it.Name, filter) {
			continue
		}
		todo = append(todo, it)
	}
	if len(todo) == 0 {
		msg := "no Kustomization from this repo"
		if filter != "" {
			msg = "no Kustomization from this repo matches " + filter
		}
		print(prefix + tui.Dim.Render(msg) + "\n")
		return
	}
	print(prefix + tui.Dim.Render(fmt.Sprintf("diffing %d Kustomizations…", len(todo))) + "\n")

	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, it := range todo {
		wg.Add(1)
		go func(it kube.FluxItem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			start := time.Now()
			name := prefix + fmt.Sprintf("%-28s ", it.Namespace+"/"+it.Name)
			dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(it.Path, "./")))
			if _, err := os.Stat(dir); err != nil {
				print(name + tui.Dim.Render("path "+it.Path+" not in this checkout") + "\n")
				return
			}
			changed, out, err := fluxDiff(fluxBin, a.src, c.Name, it, dir)
			took := tui.Dim.Render(fmt.Sprintf("  %s", time.Since(start).Round(time.Second)))
			switch {
			case err != nil:
				print(name + tui.Err.Render(err.Error()) + took + "\n")
			case !changed:
				print(name + tui.Dim.Render("no changes") + took + "\n")
			default:
				var b strings.Builder
				b.WriteString(name + tui.Warn.Render("changes") + took + "\n")
				for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
					b.WriteString("    " + colorDiff(l) + "\n")
				}
				print(b.String())
			}
		}(it)
	}
	wg.Wait()
}

// fluxDiff runs `flux diff kustomization`. Exit 0 = no changes, 1 = changes;
// anything printed with ✗ is an error.
func fluxDiff(fluxBin, kubeconfig, ctxName string, it kube.FluxItem, dir string) (changed bool, out string, err error) {
	cmd := exec.Command(fluxBin, "diff", "kustomization", it.Name, "-n", it.Namespace,
		"--path", dir, "--context", ctxName, "--progress-bar=false")
	cmd.Env = setEnv(os.Environ(), "KUBECONFIG", kubeconfig)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	runErr := cmd.Run()
	out = buf.String()
	if strings.Contains(out, "✗") {
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "✗") {
				return false, "", errors.New(truncate(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "✗")), 200))
			}
		}
	}
	var ee *exec.ExitError
	switch {
	case runErr == nil:
		return false, out, nil
	case errors.As(runErr, &ee) && ee.ExitCode() == 1:
		return true, out, nil
	}
	return false, "", errors.New(truncate(oneLine(out+" "+runErr.Error()), 200))
}

func colorDiff(l string) string {
	t := strings.TrimSpace(l)
	switch {
	case strings.HasPrefix(t, "►"):
		return tui.Bold.Render(l)
	case strings.HasPrefix(t, "+"):
		return tui.Ok.Render(l)
	case strings.HasPrefix(t, "-"):
		return tui.Err.Render(l)
	}
	return l
}

// accessError explains why a cluster can't be queried, pointing at kauth when
// the login expired.
func accessError(c kube.Context, err error) string {
	if kube.AuthExpired(err) {
		return tui.Warn.Render("login expired") + tui.Dim.Render(" · run: kauth "+c.Short)
	}
	return tui.Err.Render("no access: " + truncate(err.Error(), 120))
}
