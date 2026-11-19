package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/pathx"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
	"github.com/vitalbh/rex8s/internal/yamledit"
	rexlua "github.com/vitalbh/rex8s/rex"
)

// The k* commands are links to rex8s (kview -> rex8s), which runs the
// command of the table in commands.go their name maps to (wrapperOf); the
// guardedTools are links to rex8s-guard (production confirmation), which
// runs as the tool its name says. Both binaries dispatch on argv[0].
var guardedTools = []string{"kubectl", "helm", "flux"}

// wrapperMark is in the sh wrapper scripts older versions wrote; setup
// replaces them with links.
const wrapperMark = "# rex8s wrapper"

func rexConfigDir() string {
	if d := os.Getenv("REX_CONFIG_DIR"); d != "" {
		return d
	}
	if f := os.Getenv("REX_CONFIG"); f != "" {
		return filepath.Dir(f)
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		h, _ := os.UserHomeDir()
		base = filepath.Join(h, ".config")
	}
	return filepath.Join(base, "rex")
}

func rexInitFile() string {
	if f := os.Getenv("REX_CONFIG"); f != "" {
		return f
	}
	return filepath.Join(rexConfigDir(), "init.lua")
}

// installBinDir is where panes find rex8s (the Lua file points there).
func installBinDir() string { return filepath.Join(rexConfigDir(), "rex8s", "bin") }

const requireLine = `require("rex8s")`

// setup installs everything Rex needs, idempotently:
//
//	rex8s setup   binaries in ~/.config/rex/rex8s/bin (a link when installed
//	              by Homebrew, so upgrades apply), the k* commands and the
//	              kubectl/helm/flux guard as links to them (as the config's
//	              wrappers, skip_wrappers and guard say), rex8s.lua,
//	              require("rex8s") in init.lua, and a config reload.
func (a *app) setup(args []string) error {
	dir := installBinDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	step := func(ok bool, msg string) {
		mark := tui.Ok.Render("✓")
		if !ok {
			mark = tui.Warn.Render("!")
		}
		fmt.Println(mark + " " + msg)
	}

	// 1. the two binaries
	for _, name := range []string{"rex8s", "rex8s-guard"} {
		src := filepath.Join(a.binDir, name)
		dst := filepath.Join(dir, name)
		if samePath(src, dst) {
			step(true, name+" already in "+tildePath(dir))
			continue
		}
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("%s not found next to rex8s (%s)", name, a.binDir)
		}
		real := src // /opt/homebrew/bin/rex8s is a link into the Cellar
		if r, err := filepath.EvalSymlinks(src); err == nil {
			real = r
		}
		_ = os.Remove(dst)
		if target, ok := brewStable(real, name); ok {
			if err := os.Symlink(target, dst); err != nil {
				return err
			}
			step(true, name+" → "+target+" (link: brew upgrade applies)")
		} else {
			if err := copyFile(src, dst, 0o755); err != nil {
				return err
			}
			step(true, name+" copied to "+tildePath(dir))
		}
	}

	// 2. the production guard: asked the first time, at a terminal
	a.chooseGuard(step)

	// 3. the links: k* commands and the kubectl/helm/flux guard
	res, err := linkWrappers(dir, a.cfg)
	if err != nil {
		return err
	}
	a.reportLinks(step, dir, res)

	// 4. the Lua side: only with Rex installed (or its config already
	// there), so a machine without Rex gets just the commands
	rexBin := findRex()
	if rexBin == "" && !exists(rexInitFile()) {
		step(false, "Rex not found: installed the commands only (they work in any shell); run rex8s setup again after installing Rex")
		a.chooseAskAgent(step)
		return setupNext(dir)
	}
	luaPath := filepath.Join(rexConfigDir(), "rex8s.lua")
	if cur, err := os.ReadFile(luaPath); err == nil && string(cur) == rexlua.Source {
		step(true, "rex8s.lua up to date")
	} else {
		if err := os.WriteFile(luaPath, []byte(rexlua.Source), 0o644); err != nil {
			return err
		}
		step(true, "rex8s.lua written to "+tildePath(luaPath))
	}
	added, err := ensureRequire(rexInitFile())
	if err != nil {
		return err
	}
	if added {
		step(true, requireLine+" added to "+tildePath(rexInitFile()))
	} else {
		step(true, requireLine+" already in "+tildePath(rexInitFile()))
	}

	// 5. the agent kask opens: asked once, when both are installed
	a.chooseAskAgent(step)

	// 6. reload Rex
	if bin := rexBin; bin != "" {
		if out, err := exec.Command(bin, "config", "reload").CombinedOutput(); err != nil {
			step(false, "rex config reload failed: "+strings.TrimSpace(string(out))+" (is Rex running?)")
		} else {
			step(true, "Rex config reloaded: ⇧⌘J / ⇧⌘K / ⇧⌘M are ready")
		}
	} else {
		step(false, "rex CLI not found: reload Rex's config yourself (rex config reload)")
	}
	return setupNext(dir)
}

// setupNext: what to do after setup.
func setupNext(dir string) error {
	fmt.Println()
	fmt.Println(tui.Dim.Render("next: rex8s doctor checks the rest (kubectl, flux, claude, config)"))
	if !onPath(dir) {
		fmt.Println(tui.Dim.Render("the k* commands in any shell: echo 'export PATH=\"" + strings.Replace(dir, os.Getenv("HOME"), "$HOME", 1) + ":$PATH\"' >> ~/.zshrc"))
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst) // a new file: macOS doesn't kill a running copy
}

// wrapperNames: every name setup may link in the wrapper dir.
func wrapperNames() []string {
	names := append([]string{}, guardedTools...)
	for n := range wrapperOf {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// wrapperLink is one link setup installs: name -> target, relative.
type wrapperLink struct{ name, target string }

// wantedLinks: the links cfg asks for, by name: the kubectl/helm/flux
// guard unless guard: false, the k* commands unless wrappers: false, minus
// skip_wrappers.
func wantedLinks(cfg *config.Config) []wrapperLink {
	var out []wrapperLink
	if cfg.GuardOn() {
		for _, t := range guardedTools {
			out = append(out, wrapperLink{t, "rex8s-guard"})
		}
	}
	if cfg.WrappersOn() {
		for n := range wrapperOf {
			if !cfg.SkipWrapper(n) {
				out = append(out, wrapperLink{n, "rex8s"})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// ours: p is something setup installed: a link to rex8s or rex8s-guard, or
// an old wrapper script.
func ours(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil {
		return false
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t, err := os.Readlink(p)
		b := filepath.Base(t)
		return err == nil && (b == "rex8s" || b == "rex8s-guard")
	}
	if !fi.Mode().IsRegular() || fi.Size() > 1024 {
		return false
	}
	data, err := os.ReadFile(p)
	return err == nil && (bytes.Contains(data, []byte(wrapperMark)) || bytes.Contains(data, []byte("rex8s")))
}

// linkState: what the wrapper dir holds compared with the config.
type linkState struct {
	want            []wrapperLink
	missing, stale  int      // wanted links absent, or a script/wrong link
	extra           []string // ours, no longer wanted
	unknownSkip     []string // skip_wrappers names that are no k* command
	made, removed   int      // what linkWrappers changed
	guard, wrappers bool
	skipped         []string // skip_wrappers names left out
}

func inspectLinks(dir string, cfg *config.Config) linkState {
	st := linkState{want: wantedLinks(cfg), guard: cfg.GuardOn(), wrappers: cfg.WrappersOn()}
	wanted := map[string]bool{}
	for _, l := range st.want {
		wanted[l.name] = true
		p := filepath.Join(dir, l.name)
		t, err := os.Readlink(p)
		switch {
		case err == nil && t == l.target:
		case linkExists(p):
			st.stale++
		default:
			st.missing++
		}
	}
	for _, n := range wrapperNames() {
		if !wanted[n] && ours(filepath.Join(dir, n)) {
			st.extra = append(st.extra, n)
		}
	}
	for _, n := range cfg.SkipWrappers {
		n = strings.TrimSpace(n)
		if _, ok := wrapperOf[n]; ok {
			st.skipped = append(st.skipped, n)
		} else {
			st.unknownSkip = append(st.unknownSkip, n)
		}
	}
	return st
}

func linkExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// linkWrappers makes dir match cfg: a relative link for each wanted name
// (kview -> rex8s, kubectl -> rex8s-guard), replacing an old wrapper script
// or a wrong link in one rename, and the names it no longer wants removed.
func linkWrappers(dir string, cfg *config.Config) (linkState, error) {
	st := inspectLinks(dir, cfg)
	for _, l := range st.want {
		p := filepath.Join(dir, l.name)
		if t, err := os.Readlink(p); err == nil && t == l.target {
			continue
		}
		tmp := p + ".rex8s-new"
		_ = os.Remove(tmp)
		if err := os.Symlink(l.target, tmp); err != nil {
			return st, err
		}
		if err := os.Rename(tmp, p); err != nil {
			_ = os.Remove(tmp)
			return st, err
		}
		st.made++
	}
	for _, n := range st.extra {
		if os.Remove(filepath.Join(dir, n)) == nil {
			st.removed++
		}
	}
	return st, nil
}

// reportLinks: setup's lines about the links.
func (a *app) reportLinks(step func(bool, string), dir string, st linkState) {
	if st.guard {
		step(true, "production guard on: kubectl, helm and flux ask before changing production (rex8s config set guard false turns it off)")
	} else {
		step(true, "production guard off (guard: false): no kubectl/helm/flux in "+tildePath(dir)+", nothing asks")
	}
	k := len(st.want)
	if st.guard {
		k -= len(guardedTools)
	}
	switch {
	case !st.wrappers:
		step(true, "k* commands off (wrappers: false): rex8s kview, rex8s logs… still work")
	case len(st.skipped) > 0:
		step(true, fmt.Sprintf("%d k* commands in %s (left out: %s)", k, tildePath(dir), strings.Join(st.skipped, ", ")))
	default:
		step(true, fmt.Sprintf("%d k* commands in %s", k, tildePath(dir)))
	}
	if len(st.unknownSkip) > 0 {
		step(false, "skip_wrappers: not k* commands, ignored: "+strings.Join(st.unknownSkip, ", "))
	}
	if st.removed > 0 {
		step(true, "removed what the config no longer wants: "+strings.Join(st.extra, ", "))
	}
}

// ensureRequire adds require("rex8s") to init.lua (creating it if needed).
func ensureRequire(initFile string) (bool, error) {
	data, err := os.ReadFile(initFile)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	for _, l := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "--") {
			continue
		}
		if strings.Contains(t, `require("rex8s")`) || strings.Contains(t, `require "rex8s"`) || strings.Contains(t, `require('rex8s')`) {
			return false, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(initFile), 0o755); err != nil {
		return false, err
	}
	s := string(data)
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	if s != "" {
		s += "\n"
	}
	s += "-- rex8s: one Kubernetes cluster per pane (rex8s uninstall removes this)\n" + requireLine + "\n"
	return true, os.WriteFile(initFile, []byte(s), 0o644)
}

// uninstall undoes setup; the config (config.yaml) and caches stay.
func (a *app) uninstall(args []string) error {
	dir := installBinDir()
	removed := 0
	for _, n := range wrapperNames() {
		if p := filepath.Join(dir, n); ours(p) && os.Remove(p) == nil {
			removed++
		}
	}
	for _, n := range []string{"rex8s", "rex8s-guard"} {
		_ = os.Remove(filepath.Join(dir, n))
	}
	fmt.Printf("%s removed %d commands and the binaries from %s\n", tui.Ok.Render("✓"), removed, tildePath(dir))
	_ = os.Remove(filepath.Join(rexConfigDir(), "rex8s.lua"))
	fmt.Println(tui.Ok.Render("✓") + " removed rex8s.lua")
	if data, err := os.ReadFile(rexInitFile()); err == nil {
		var keep []string
		for _, l := range strings.Split(string(data), "\n") {
			t := strings.TrimSpace(l)
			if strings.Contains(t, `require("rex8s")`) || strings.HasPrefix(t, "-- rex8s: one Kubernetes cluster per pane") {
				continue
			}
			keep = append(keep, l)
		}
		if err := os.WriteFile(rexInitFile(), []byte(strings.Join(keep, "\n")), 0o644); err == nil {
			fmt.Println(tui.Ok.Render("✓") + " " + requireLine + " removed from " + tildePath(rexInitFile()))
		}
	}
	if bin := findRex(); bin != "" {
		_ = exec.Command(bin, "config", "reload").Run()
	}
	fmt.Println(tui.Dim.Render("kept: " + tildePath(a.cfg.File()) + " (your config) · remove the PATH line from your shell rc if you added it"))
	return nil
}

// findRex: rex on PATH, else inside the Rex app (a variable: tests stub it).
var findRex = func() string {
	if p, err := exec.LookPath("rex"); err == nil {
		return p
	}
	b := rex.Bin()
	if _, err := os.Stat(b); err == nil {
		return b
	}
	return ""
}

func onPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if samePath(p, dir) {
			return true
		}
	}
	return false
}

// doctor checks the installation and the tools rex8s uses.
func (a *app) doctor(args []string) error {
	d := &checker{}
	d.section("rex8s")
	a.doctorInstall(d)
	d.section("Rex")
	doctorRex(d)
	d.section("tools")
	a.doctorTools(d)
	d.section("config")
	a.doctorConfig(d)
	d.section("shell")
	doctorShell(d)
	d.section("agents")
	for _, s := range agentSkillDirs() {
		d.check(s.state == "current", true, "skill in "+tildePath(s.dir), map[string]string{"current": "", "outdated": "rex8s agents install", "missing": "rex8s agents install (optional)"}[s.state])
	}

	fmt.Println()
	if d.bad > 0 {
		return fmt.Errorf("%d problem(s) above", d.bad)
	}
	fmt.Println(tui.Ok.Render("all good"))
	return nil
}

// checker prints doctor's lines and counts the problems.
type checker struct{ bad int }

// check prints ✓, ! (a warning: warnOnly) or ✗ (a problem) before what.
func (d *checker) check(ok bool, warnOnly bool, what, detail string) {
	mark := tui.Ok.Render("✓")
	switch {
	case !ok && warnOnly:
		mark = tui.Warn.Render("!")
	case !ok:
		mark = tui.Err.Render("✗")
		d.bad++
	}
	if detail != "" {
		detail = tui.Dim.Render("  " + detail)
	}
	fmt.Println(mark + " " + what + detail)
}

func (d *checker) section(s string) { fmt.Println("\n" + tui.Bold.Render(s)) }

// unless is detail when !ok, "" when ok.
func unless(ok bool, detail string) string {
	if ok {
		return ""
	}
	return detail
}

// doctorInstall: the binaries and the wrappers setup installs.
func (a *app) doctorInstall(d *checker) {
	d.check(true, false, "rex8s "+version, a.binDir)
	dir := installBinDir()
	_, err := os.Stat(filepath.Join(dir, "rex8s"))
	d.check(err == nil, false, "installed in "+tildePath(dir), unless(err == nil, "run rex8s setup"))
	st := inspectLinks(dir, a.cfg)
	ok := st.missing+st.stale+len(st.extra) == 0
	what := fmt.Sprintf("%d commands linked (k*, kubectl/helm/flux guard)", len(st.want))
	switch {
	case !st.wrappers && !st.guard:
		what = "no commands linked (wrappers: false, guard: false)"
	case !st.wrappers:
		what = "kubectl/helm/flux guard linked (wrappers: false: no k* commands)"
	case !st.guard:
		what = fmt.Sprintf("%d k* commands linked", len(st.want))
	}
	d.check(ok, false, what,
		unless(ok, fmt.Sprintf("%d missing, %d outdated, %d to remove: rex8s setup", st.missing, st.stale, len(st.extra))))
	if st.guard {
		d.check(true, false, "guard: on", "kubectl/helm/flux ask before changing production")
	} else {
		d.check(false, true, "guard: off", "guard: false in config.yaml: nothing asks before changing production")
	}
	if len(st.unknownSkip) > 0 {
		d.check(false, true, "skip_wrappers", "not k* commands: "+strings.Join(st.unknownSkip, ", "))
	}
	d.check(onPath(dir), true, "on this shell's PATH", unless(onPath(dir), "only rex8s panes have them; see docs/configuration.md › In any shell"))
}

// doctorRex: the rex CLI, the server, rex8s.lua and init.lua.
func doctorRex(d *checker) {
	rb := findRex()
	if rb == "" {
		fmt.Println(tui.Dim.Render("· Rex not installed: the k* commands work in any shell; cluster panes, shortcuts and splits need Rex (then rex8s setup again)"))
		return
	}
	d.check(true, false, "rex CLI", rb)
	out, err := exec.Command(rb, "ls", "--json").CombinedOutput()
	d.check(err == nil, false, "Rex server answers", unless(err == nil, strings.TrimSpace(string(out))))
	if err == nil {
		rev := rex.APIRevision()
		ok := rev >= rex.PromptAPIRevision
		d.check(ok, true, fmt.Sprintf("palette questions (Rex API revision %d)", rev),
			unless(ok, "Rex before preview.21: the palette actions open rex8s pickers in a pane, and the guard refuses agents instead of asking in the palette (update Rex, then restart its server)"))
	}
	lua, err := os.ReadFile(filepath.Join(rexConfigDir(), "rex8s.lua"))
	switch {
	case err != nil:
		d.check(false, false, "rex8s.lua", "missing: rex8s setup")
	case string(lua) != rexlua.Source:
		d.check(false, false, "rex8s.lua", "outdated (another rex8s version wrote it): rex8s setup")
	default:
		d.check(true, false, "rex8s.lua up to date", "")
	}
	init, _ := os.ReadFile(rexInitFile())
	d.check(strings.Contains(string(init), "rex8s"), false, requireLine+" in "+tildePath(rexInitFile()), "")
	fmt.Println(tui.Dim.Render("· Raycast can show the tab it opens only with Remote Control on (Rex › Settings › Rex Server); not checkable from here"))
}

// doctorTools: kubectl and the optional tools, with their versions.
func (a *app) doctorTools(d *checker) {
	tool := func(name string, required bool, why string, versionArgs ...string) {
		p := pathx.Real(name, a.binDir)
		if p == "" {
			d.check(false, !required, name, "not found: "+why)
			return
		}
		v := ""
		if len(versionArgs) > 0 {
			if out, err := exec.Command(p, versionArgs...).Output(); err == nil {
				v = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
			}
		}
		d.check(true, false, name, strings.TrimSpace(v+"  "+p))
	}
	tool("kubectl", true, "everything needs it", "version", "--client")
	tool("flux", false, "fdiff needs it (brew install fluxcd/tap/flux)", "--version")
	tool("k9s", false, "optional: cluster_browser: k9s puts it in the ⇧⌘K layout (kview by default)", "version", "--short")
	tool("claude", false, "kwhy and kask need it (or codex for kask)", "--version")
	tool("codex", false, "optional: kask can use it (rex8s config set ask_agent codex)", "--version")
	tool("gh", false, "optional", "--version")
	tool("git", false, "fwatch, fdiff and kgit need it", "--version")
}

// doctorConfig: the config file, the kubeconfig, the GitOps clones and
// the favorite forwards' clusters.
func (a *app) doctorConfig(d *checker) {
	d.check(true, false, tildePath(a.cfg.File()), unless(fileExists(a.cfg.File()), "not created yet: everything is optional"))
	raw, err := kube.Load(a.src)
	if err != nil {
		d.check(false, false, "kubeconfig "+tildePath(a.src), err.Error())
	} else {
		cs := kube.Contexts(raw, a.cfg)
		prod := 0
		for _, c := range cs {
			if c.Prod {
				prod++
			}
		}
		d.check(len(cs) > 0, false, fmt.Sprintf("kubeconfig %s: %d contexts, %d production", tildePath(a.src), len(cs), prod),
			unless(len(cs) > 0, "no contexts"))
		marked := prod > 0 || len(a.cfg.Clusters) > 0
		d.check(marked, true, "production rules", unless(marked, "no cluster is marked prod: add clusters: in the config"))
	}
	for _, dir := range a.cfg.GitopsDirs() {
		d.check(fileExists(filepath.Join(dir, ".git")), true, "gitops_repos "+tildePath(dir), "")
	}
	for _, f := range a.cfg.Forwards {
		_, err := a.context(f.Cluster)
		d.check(err == nil, true, "favorite forward "+f.Name, unless(err == nil, "cluster "+f.Cluster+" not found"))
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// configPath: the config file, even when it doesn't exist yet.
func (a *app) configPath() string {
	if p := a.cfg.File(); p != "" {
		return p
	}
	return config.Path()
}

// chooseAskAgent: with claude and codex both installed and no ask_agent yet,
// asks which one kask should open and saves it. With one, nothing to ask.
func (a *app) chooseAskAgent(step func(bool, string)) {
	if a.cfg.AskAgent != "" {
		step(true, "kask opens "+a.cfg.AskAgent+" (rex8s config set ask_agent claude|codex)")
		return
	}
	claude, codex := pathx.Real("claude", a.binDir) != "", pathx.Real("codex", a.binDir) != ""
	switch {
	case !claude && !codex:
		step(false, "no claude or codex CLI: kask and kwhy need one")
		return
	case !(claude && codex):
		step(true, "kask opens "+a.askAgent()+" (the one installed)")
		return
	case !stdinTTY():
		step(true, "kask opens claude (rex8s config set ask_agent codex to change)")
		return
	}
	it, err := tui.Run(tui.Options{Title: "kask: which agent should it open?", Items: []tui.Item{
		{Key: "claude", Label: "claude", Hint: "Claude Code · read-only kubectl runs without asking"},
		{Key: "codex", Label: "codex ", Hint: "Codex · asks before each command"},
	}})
	if err != nil || it == nil {
		step(true, "kask opens claude (rex8s config set ask_agent codex to change)")
		return
	}
	if err := a.editConfig(func(doc string) string { return yamledit.SetKey(doc, "ask_agent", it.Key) }); err != nil {
		step(false, "could not save ask_agent: "+err.Error())
		return
	}
	step(true, "kask opens "+it.Key+" (saved; rex8s config set ask_agent to change)")
}

// stdinTTY: someone at a terminal can answer setup's questions (tests stub it).
var stdinTTY = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// askGuard asks whether to install the production guard; answered is false
// when the picker was cancelled (tests stub it).
var askGuard = func() (on, answered bool) {
	it, err := tui.Run(tui.Options{Title: "Install the production guard? kubectl/helm/flux ask before changing production clusters (recommended)", Items: []tui.Item{
		{Key: "yes", Label: "Yes", Hint: "kubectl, helm and flux ask before changing a production cluster"},
		{Key: "no", Label: "No ", Hint: "guard: false · nothing asks; turn it on later: rex8s config set guard true"},
	}})
	if err != nil || it == nil {
		return true, false
	}
	return it.Key == "yes", true
}

// chooseGuard: with no guard key in the config yet and someone at the
// terminal, asks whether to install the production guard and saves the
// answer, so it is asked once. Without a terminal (brew, a script, curl on a
// server) it stays on and nothing is saved.
func (a *app) chooseGuard(step func(bool, string)) {
	if a.cfg.Guard != nil || !stdinTTY() {
		return
	}
	on, answered := askGuard()
	if !answered {
		return // on, and asked again next time
	}
	val := strconv.FormatBool(on)
	if err := a.editConfig(func(doc string) string { return yamledit.SetKey(doc, "guard", val) }); err != nil {
		step(false, "could not save guard: "+err.Error())
		a.cfg.Guard = &on // still applies to this setup
		return
	}
	if a.cfg.Guard == nil { // "nothing to change": keep the answer for this run
		a.cfg.Guard = &on
	}
}

// brewStable: for a Homebrew install (…/Cellar/rex8s/<version>/bin/NAME),
// the path that survives upgrades: …/opt/rex8s/bin/NAME.
func brewStable(src, name string) (string, bool) {
	i := strings.Index(src, "/Cellar/")
	if i < 0 {
		return "", false
	}
	rest := strings.SplitN(src[i+len("/Cellar/"):], "/", 2) // formula, version/…
	return filepath.Join(src[:i], "opt", rest[0], "bin", name), true
}
