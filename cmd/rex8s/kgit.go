package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// kgit answers "where do I change this?" for a workload Flux applied: the
// Kustomization (and HelmRelease) behind it, the Git repository and path,
// and — with a local clone — the file that defines it. Enter opens it.
//
//	kgit [FILTER]
//
// Local clones: the current directory's repository, or config gitops_repos.
func (a *app) kgit(args []string) error {
	filter := strings.Join(args, " ")
	c, err := kube.Current(a.cfg, "", "")
	if err != nil {
		return err
	}
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	cl.Timeout = 30 * time.Second
	ws := map[string]fluxOwned{}
	it, err := tui.Run(tui.Options{
		Title: "Where is it in Git? · workloads on " + c.Short,
		Query: filter,
		Load: func() ([]tui.Item, error) {
			out, err := cl.Get(c.Name, "get", "deployments,statefulsets,daemonsets", "-A", "-o", "json")
			if err != nil {
				return nil, err
			}
			list, err := parseFluxOwned(out)
			if err != nil {
				return nil, err
			}
			w := 0
			for _, o := range list {
				w = max(w, len(o.Namespace)+1+len(o.Name))
			}
			items := make([]tui.Item, 0, len(list))
			for _, o := range list {
				key := o.Kind + "/" + o.Namespace + "/" + o.Name
				ws[key] = o
				items = append(items, tui.Item{Key: key, Label: fmt.Sprintf("%-6s %-*s", kube.ShortKind(o.Kind), w, o.Namespace+"/"+o.Name), Hint: o.owner()})
			}
			return items, nil
		},
	})
	if err != nil || it == nil {
		return err
	}
	o := ws[it.Key]
	fmt.Fprint(os.Stderr, tui.Dim.Render("following the Flux chain… "))
	chain, err := resolveFluxChain(cl, c.Name, o)
	fmt.Fprint(os.Stderr, "\r\033[2K")
	if err != nil {
		return err
	}
	fmt.Print(chain.describe())

	clone := a.findClone(chain.repoURL)
	if clone == "" {
		web := webURL(chain.repoURL, chain.branch, chain.path, true)
		fmt.Println(tui.Warn.Render("\nno local clone of " + chain.repoURL))
		fmt.Println(tui.Dim.Render("clone it and add it to gitops_repos in " + a.cfg.File() + " (or run kgit inside it) to find the file"))
		if web != "" {
			fmt.Println("folder: " + web)
			return a.openChoice([]gitHit{{web: web}})
		}
		return nil
	}
	hits := findDefinitions(clone, chain)
	if len(hits) == 0 {
		fmt.Println(tui.Warn.Render(fmt.Sprintf("\nnot found in %s (looked for %s %s)", tildePath(clone), chain.lookKind, chain.lookName)))
		return nil
	}
	fmt.Println()
	for i := range hits {
		hits[i].web = webURL(chain.repoURL, chain.branch, hits[i].rel, false)
		if hits[i].web != "" && hits[i].line > 0 && strings.Contains(hits[i].web, "github.com") {
			hits[i].web += fmt.Sprintf("#L%d", hits[i].line)
		}
		fmt.Printf("  %s:%d\n", tui.Bold.Render(hits[i].rel), hits[i].line)
	}
	return a.openChoice(hits)
}

type fluxOwned struct {
	Kind, Namespace, Name string
	KsName, KsNamespace   string
	HrName, HrNamespace   string
}

func (o fluxOwned) owner() string {
	switch {
	case o.HrName != "":
		return "helm " + o.HrNamespace + "/" + o.HrName
	case o.KsName != "":
		return "ks " + o.KsNamespace + "/" + o.KsName
	}
	return "not applied by Flux"
}

func parseFluxOwned(data []byte) ([]fluxOwned, error) {
	var list struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string            `json:"name"`
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]fluxOwned, 0, len(list.Items))
	for _, it := range list.Items {
		l := it.Metadata.Labels
		out = append(out, fluxOwned{Kind: it.Kind, Namespace: it.Metadata.Namespace, Name: it.Metadata.Name,
			KsName: l["kustomize.toolkit.fluxcd.io/name"], KsNamespace: l["kustomize.toolkit.fluxcd.io/namespace"],
			HrName: l["helm.toolkit.fluxcd.io/name"], HrNamespace: l["helm.toolkit.fluxcd.io/namespace"]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

type fluxChain struct {
	workload           fluxOwned
	hr                 string // ns/name when a HelmRelease made it
	chart              string
	ks                 string // ns/name
	path               string // Kustomization spec.path
	source             string // kind ns/name
	repoURL, branch    string
	revision           string
	lookKind, lookName string // what defines the workload in Git
}

func (c fluxChain) describe() string {
	var b strings.Builder
	w := c.workload
	fmt.Fprintf(&b, "%s %s/%s\n", tui.Bold.Render(kube.ShortKind(w.Kind)), w.Namespace, w.Name)
	if c.hr != "" {
		fmt.Fprintf(&b, "  ← HelmRelease   %s %s\n", c.hr, tui.Dim.Render(c.chart))
	}
	fmt.Fprintf(&b, "  ← Kustomization %s %s\n", c.ks, tui.Dim.Render("path "+c.path))
	fmt.Fprintf(&b, "  ← %s %s\n", c.source, tui.Dim.Render(c.repoURL+" @ "+firstNonEmpty(c.branch, "default branch")+" "+shortRev(c.revision)))
	return b.String()
}

func shortRev(r string) string {
	if i := strings.LastIndex(r, ":"); i >= 0 {
		r = r[i+1:]
	}
	if len(r) > 10 {
		r = r[:10]
	}
	return r
}

func getJSON(cl kube.Client, ctxName string, v any, args ...string) error {
	out, err := cl.Get(ctxName, args...)
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

// resolveFluxChain: workload → (HelmRelease →) Kustomization → source.
func resolveFluxChain(cl kube.Client, ctxName string, w fluxOwned) (fluxChain, error) {
	ch := fluxChain{workload: w, lookKind: w.Kind, lookName: w.Name}
	ksName, ksNS := w.KsName, w.KsNamespace
	if w.HrName != "" {
		var hr struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Chart struct {
					Spec struct {
						Chart   string `json:"chart"`
						Version string `json:"version"`
					} `json:"spec"`
				} `json:"chart"`
			} `json:"spec"`
		}
		if err := getJSON(cl, ctxName, &hr, "get", kube.HelmReleaseRes, w.HrName, "-n", w.HrNamespace, "-o", "json"); err != nil {
			return ch, fmt.Errorf("HelmRelease %s/%s: %v", w.HrNamespace, w.HrName, lastLogLine(err.Error()))
		}
		ch.hr = w.HrNamespace + "/" + w.HrName
		ch.chart = strings.TrimSpace(hr.Spec.Chart.Spec.Chart + " " + hr.Spec.Chart.Spec.Version)
		ch.lookKind, ch.lookName = "HelmRelease", w.HrName
		ksName, ksNS = hr.Metadata.Labels["kustomize.toolkit.fluxcd.io/name"], hr.Metadata.Labels["kustomize.toolkit.fluxcd.io/namespace"]
	}
	if ksName == "" {
		return ch, errors.New(w.Namespace + "/" + w.Name + " was not applied by a Flux Kustomization (no kustomize.toolkit.fluxcd.io labels)")
	}
	var ks struct {
		Spec struct {
			Path      string `json:"path"`
			SourceRef struct {
				Kind      string `json:"kind"`
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"sourceRef"`
		} `json:"spec"`
	}
	if err := getJSON(cl, ctxName, &ks, "get", kube.KustomizationRes, ksName, "-n", ksNS, "-o", "json"); err != nil {
		return ch, fmt.Errorf("Kustomization %s/%s: %v", ksNS, ksName, lastLogLine(err.Error()))
	}
	ch.ks, ch.path = ksNS+"/"+ksName, firstNonEmpty(ks.Spec.Path, "./")
	ref := ks.Spec.SourceRef
	srcNS := firstNonEmpty(ref.Namespace, ksNS)
	ch.source = ref.Kind + " " + srcNS + "/" + ref.Name
	if ref.Kind != "GitRepository" {
		return ch, fmt.Errorf("source is a %s: only GitRepository sources point at a Git file", ref.Kind)
	}
	var src struct {
		Spec struct {
			URL string `json:"url"`
			Ref struct {
				Branch string `json:"branch"`
				Tag    string `json:"tag"`
			} `json:"ref"`
		} `json:"spec"`
		Status struct {
			Artifact struct {
				Revision string `json:"revision"`
			} `json:"artifact"`
		} `json:"status"`
	}
	if err := getJSON(cl, ctxName, &src, "get", kube.GitRepositoryRes, ref.Name, "-n", srcNS, "-o", "json"); err != nil {
		return ch, fmt.Errorf("GitRepository %s/%s: %v", srcNS, ref.Name, lastLogLine(err.Error()))
	}
	ch.repoURL, ch.branch, ch.revision = src.Spec.URL, firstNonEmpty(src.Spec.Ref.Branch, src.Spec.Ref.Tag), src.Status.Artifact.Revision
	return ch, nil
}

// normRepo: git@github.com:org/repo.git, https://github.com/org/repo,
// ssh://git@github.com/org/repo → github.com/org/repo
func normRepo(u string) string {
	u = strings.TrimSpace(strings.ToLower(u))
	for _, p := range []string{"https://", "http://", "ssh://", "git://"} {
		u = strings.TrimPrefix(u, p)
	}
	if i := strings.Index(u, "@"); i >= 0 {
		u = u[i+1:]
	}
	if i := strings.Index(u, ":"); i >= 0 && !strings.Contains(u[:i], "/") {
		rest := u[i+1:]
		if j := strings.Index(rest, "/"); j >= 0 && regexp.MustCompile(`^\d+$`).MatchString(rest[:j]) {
			rest = rest[j+1:] // host:port/path
		}
		u = u[:i] + "/" + rest
	}
	return strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
}

// findClone: a local repository whose remote is the GitRepository's URL.
func (a *app) findClone(repoURL string) string {
	want := normRepo(repoURL)
	dirs := a.cfg.GitopsDirs()
	if top, err := git("rev-parse", "--show-toplevel"); err == nil {
		dirs = append([]string{top}, dirs...)
	}
	for _, d := range dirs {
		out, err := exec.Command("git", "-C", d, "remote", "-v").Output()
		if err != nil {
			continue
		}
		for _, l := range strings.Split(string(out), "\n") {
			if f := strings.Fields(l); len(f) >= 2 && normRepo(f[1]) == want {
				if top, err := exec.Command("git", "-C", d, "rev-parse", "--show-toplevel").Output(); err == nil {
					return strings.TrimSpace(string(top))
				}
				return d
			}
		}
	}
	return ""
}

type gitHit struct {
	file, rel string
	line      int
	web       string
}

var docSep = regexp.MustCompile(`(?m)^---[ \t]*$`)

// findDefinitions looks for the YAML document defining (kind, name) under
// the Kustomization's path, then in the whole repository (bases often live
// outside it).
func findDefinitions(clone string, ch fluxChain) []gitHit {
	root := filepath.Join(clone, filepath.FromSlash(strings.TrimPrefix(ch.path, "./")))
	hits := searchYAML(clone, root, ch.lookKind, ch.lookName)
	if len(hits) == 0 && root != clone {
		hits = searchYAML(clone, clone, ch.lookKind, ch.lookName)
	}
	return hits
}

func searchYAML(clone, root, kind, name string) []gitHit {
	var hits []gitHit
	nameLine := regexp.MustCompile(`(?m)^\s*name:\s*["']?` + regexp.QuoteMeta(name) + `["']?\s*$`)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if ext := filepath.Ext(p); ext != ".yaml" && ext != ".yml" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil || !bytes.Contains(data, []byte(name)) {
			return nil
		}
		text := string(data)
		starts := []int{0}
		for _, loc := range docSep.FindAllStringIndex(text, -1) {
			starts = append(starts, loc[1])
		}
		for i, start := range starts {
			end := len(text)
			if i+1 < len(starts) {
				end = docSep.FindAllStringIndex(text, -1)[i][0]
			}
			doc := text[start:end]
			var head struct {
				Kind     string `json:"kind"`
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
			}
			if yaml.Unmarshal([]byte(doc), &head) != nil || head.Kind != kind || head.Metadata.Name != name {
				continue
			}
			line := strings.Count(text[:start], "\n") + 1
			if loc := nameLine.FindStringIndex(doc); loc != nil {
				line += strings.Count(doc[:loc[0]], "\n")
			}
			rel, _ := filepath.Rel(clone, p)
			hits = append(hits, gitHit{file: p, rel: filepath.ToSlash(rel), line: line})
		}
		return nil
	})
	return hits
}

// webURL: a GitHub/GitLab link to a file (or a folder) on the branch.
func webURL(repoURL, branch, rel string, folder bool) string {
	n := normRepo(repoURL)
	if !strings.HasPrefix(n, "github.com/") && !strings.HasPrefix(n, "gitlab.com/") {
		return ""
	}
	if branch == "" {
		branch = "HEAD"
	}
	kind := "blob"
	if folder {
		kind = "tree"
	}
	rel = strings.TrimPrefix(strings.TrimPrefix(rel, "./"), "/")
	sep := "/"
	if strings.HasPrefix(n, "gitlab.com/") {
		sep = "/-/"
	}
	return "https://" + n + sep + kind + "/" + branch + "/" + rel
}

// openChoice: open in the editor, on the web, or copy the path.
func (a *app) openChoice(hits []gitHit) error {
	h := hits[0]
	if len(hits) > 1 {
		items := make([]tui.Item, len(hits))
		for i, x := range hits {
			items[i] = tui.Item{Key: fmt.Sprint(i), Label: fmt.Sprintf("%s:%d", x.rel, x.line)}
		}
		it, err := tui.Run(tui.Options{Title: "Which file?", Items: items})
		if err != nil || it == nil {
			return err
		}
		var i int
		fmt.Sscan(it.Key, &i)
		h = hits[i]
	}
	var acts []tui.Item
	if h.file != "" {
		acts = append(acts, tui.Item{Key: "edit", Label: "edit  ", Hint: "open in your editor at the line"})
	}
	if h.web != "" {
		acts = append(acts, tui.Item{Key: "web", Label: "web   ", Hint: h.web})
	}
	if h.file != "" {
		acts = append(acts, tui.Item{Key: "copy", Label: "copy  ", Hint: "copy the path"})
	}
	if len(acts) == 0 {
		return nil
	}
	act, err := tui.Run(tui.Options{Title: firstNonEmpty(h.rel, h.web), Items: acts})
	if err != nil || act == nil {
		return err
	}
	switch act.Key {
	case "web":
		return exec.Command("open", h.web).Run()
	case "copy":
		cmd := exec.Command("pbcopy")
		cmd.Stdin = strings.NewReader(h.file)
		return cmd.Run()
	}
	return openInEditor(h.file, h.line)
}

// openInEditor: VS Code / Cursor / Zed at the line when installed, else
// $VISUAL/$EDITOR (in a split when inside Rex), else the system default.
func openInEditor(file string, line int) error {
	for _, ed := range []string{"cursor", "code", "zed"} {
		if p, err := exec.LookPath(ed); err == nil {
			arg := fmt.Sprintf("%s:%d", file, line)
			if ed == "zed" {
				return exec.Command(p, arg).Run()
			}
			return exec.Command(p, "-g", arg).Run()
		}
	}
	if ed := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR")); ed != "" {
		argv := append(strings.Fields(ed), fmt.Sprintf("+%d", line), file)
		if rex.Block() != "" {
			return rex.Split("edit "+filepath.Base(file), argv...)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	return exec.Command("open", file).Run()
}
