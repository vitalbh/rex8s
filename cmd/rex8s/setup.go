package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/term"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/pathx"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
	rexlua "github.com/vitalbh/rex8s/rex"
)

// wrapperCmds: the commands panes have on PATH → the rex8s subcommand each
// one runs. The single list: setup writes them, doctor checks them.
var wrapperCmds = map[string]string{
	"kctx": "switch", "kns": "ns", "kflux": "flux", "klogs": "logs",
	"fwatch": "fwatch", "fdiff": "fdiff", "kpf": "kpf", "kauth": "kauth", "kexec": "kexec",
	"kwarn": "kwarn", "kwhy": "kwhy", "kfail": "kfail", "kjobs": "kjobs", "kstatus": "kstatus",
	"krun": "krun", "kver": "kver", "kroll": "kroll", "kdiff": "kdiff", "knode": "knode",
	"kask": "kask", "ktint": "ktint", "khelp": "khelp", "knet": "knet", "kwhat": "kwhat",
	"kgrep": "kgrep", "kcron": "kcron", "kreq": "kreq", "kincident": "kincident",
	"kdisk": "kdisk", "kgit": "kgit", "kjunk": "kjunk", "kmulti": "kmulti", "kall": "kall",
}

// guardedTools go through rex8s-guard (production confirmation).
var guardedTools = []string{"kubectl", "helm", "flux"}

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
//	              by Homebrew, so upgrades apply), the k* wrappers, rex8s.lua,
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
		_ = os.Remove(dst)
		if target, ok := brewStable(src, name); ok {
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

	// 2. wrappers
	n, err := writeWrappers(dir)
	if err != nil {
		return err
	}
	step(true, fmt.Sprintf("%d commands in %s (k*, kubectl/helm/flux guard)", n, tildePath(dir)))

	// 3. the Lua side
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

	// 4. the agent kask opens: asked once, when both are installed
	a.chooseAskAgent(step)

	// 5. reload Rex
	if bin := findRex(); bin != "" {
		if out, err := exec.Command(bin, "config", "reload").CombinedOutput(); err != nil {
			step(false, "rex config reload failed: "+strings.TrimSpace(string(out))+" (is Rex running?)")
		} else {
			step(true, "Rex config reloaded: ⇧⌘J / ⇧⌘K / ⇧⌘M are ready")
		}
	} else {
		step(false, "rex CLI not found: reload Rex's config yourself (rex config reload)")
	}

	fmt.Println()
	fmt.Println(tui.Dim.Render("next: rex8s doctor checks the rest (kubectl, flux, claude, config)"))
	if !onPath(dir) {
		fmt.Println(tui.Dim.Render("the k* commands in any shell: echo 'export PATH=\"" + strings.Replace(dir, os.Getenv("HOME"), "$HOME", 1) + ":$PATH\"' >> ~/.zshrc"))
	}
	return nil
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

func wrapperScript(name string) string {
	if sub, ok := wrapperCmds[name]; ok {
		return fmt.Sprintf("#!/bin/sh\n%s\nexec \"$(dirname \"$0\")/rex8s\" %s \"$@\"\n", wrapperMark, sub)
	}
	return fmt.Sprintf("#!/bin/sh\n%s\nexec \"$(dirname \"$0\")/rex8s-guard\" %s \"$@\"\n", wrapperMark, name)
}

func wrapperNames() []string {
	names := append([]string{}, guardedTools...)
	for n := range wrapperCmds {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func writeWrappers(dir string) (int, error) {
	names := wrapperNames()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(wrapperScript(n)), 0o755); err != nil {
			return 0, err
		}
	}
	return len(names), nil
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
		p := filepath.Join(dir, n)
		if data, err := os.ReadFile(p); err == nil && (bytes.Contains(data, []byte(wrapperMark)) || bytes.Contains(data, []byte("rex8s"))) {
			if os.Remove(p) == nil {
				removed++
			}
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

// findRex: rex on PATH, else inside the Rex app.
func findRex() string {
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
	bad := 0
	check := func(ok bool, warnOnly bool, what, detail string) {
		mark := tui.Ok.Render("✓")
		switch {
		case !ok && warnOnly:
			mark = tui.Warn.Render("!")
		case !ok:
			mark = tui.Err.Render("✗")
			bad++
		}
		if detail != "" {
			detail = tui.Dim.Render("  " + detail)
		}
		fmt.Println(mark + " " + what + detail)
	}
	section := func(s string) { fmt.Println("\n" + tui.Bold.Render(s)) }

	section("rex8s")
	check(true, false, "rex8s "+version, a.binDir)
	dir := installBinDir()
	_, err := os.Stat(filepath.Join(dir, "rex8s"))
	check(err == nil, false, "installed in "+tildePath(dir), map[bool]string{true: "", false: "run rex8s setup"}[err == nil])
	missing, stale := 0, 0
	for _, n := range wrapperNames() {
		data, err := os.ReadFile(filepath.Join(dir, n))
		switch {
		case err != nil:
			missing++
		case string(data) != wrapperScript(n):
			stale++
		}
	}
	check(missing+stale == 0, false, fmt.Sprintf("%d commands (k*, kubectl/helm/flux guard)", len(wrapperNames())),
		map[bool]string{true: "", false: fmt.Sprintf("%d missing, %d outdated: rex8s setup", missing, stale)}[missing+stale == 0])
	check(onPath(dir), true, "on this shell's PATH", map[bool]string{true: "", false: "only rex8s panes have them; see README › In any shell"}[onPath(dir)])

	section("Rex")
	rb := findRex()
	check(rb != "", false, "rex CLI", rb)
	if rb != "" {
		out, err := exec.Command(rb, "ls", "--json").CombinedOutput()
		check(err == nil, false, "Rex server answers", map[bool]string{true: "", false: strings.TrimSpace(string(out))}[err == nil])
	}
	lua, err := os.ReadFile(filepath.Join(rexConfigDir(), "rex8s.lua"))
	switch {
	case err != nil:
		check(false, false, "rex8s.lua", "missing: rex8s setup")
	case string(lua) != rexlua.Source:
		check(false, false, "rex8s.lua", "outdated (another rex8s version wrote it): rex8s setup")
	default:
		check(true, false, "rex8s.lua up to date", "")
	}
	init, _ := os.ReadFile(rexInitFile())
	check(strings.Contains(string(init), "rex8s"), false, requireLine+" in "+tildePath(rexInitFile()), "")
	fmt.Println(tui.Dim.Render("· Raycast can show the tab it opens only with Remote Control on (Rex › Settings › Rex Server); not checkable from here"))

	section("tools")
	tool := func(name string, required bool, why string, versionArgs ...string) {
		p := pathx.Real(name, a.binDir)
		if p == "" {
			check(false, !required, name, "not found: "+why)
			return
		}
		v := ""
		if len(versionArgs) > 0 {
			if out, err := exec.Command(p, versionArgs...).Output(); err == nil {
				v = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
			}
		}
		check(true, false, name, strings.TrimSpace(v+"  "+p))
	}
	tool("kubectl", true, "everything needs it", "version", "--client")
	tool("flux", false, "fdiff needs it (brew install fluxcd/tap/flux)", "--version")
	tool("k9s", false, "the ⇧⌘K layout shows kstatus instead", "version", "--short")
	tool("claude", false, "kwhy and kask need it (or codex for kask)", "--version")
	tool("codex", false, "optional: kask can use it (rex8s config set ask_agent codex)", "--version")
	tool("gh", false, "optional", "--version")
	tool("git", false, "fwatch, fdiff and kgit need it", "--version")

	section("config")
	check(true, false, tildePath(a.cfg.File()), map[bool]string{true: "", false: "not created yet: everything is optional"}[fileExists(a.cfg.File())])
	raw, err := kube.Load(a.src)
	if err != nil {
		check(false, false, "kubeconfig "+tildePath(a.src), err.Error())
	} else {
		cs := kube.Contexts(raw, a.cfg)
		prod := 0
		for _, c := range cs {
			if c.Prod {
				prod++
			}
		}
		check(len(cs) > 0, false, fmt.Sprintf("kubeconfig %s: %d contexts, %d production", tildePath(a.src), len(cs), prod),
			map[bool]string{true: "", false: "no contexts"}[len(cs) > 0])
		check(prod > 0 || len(a.cfg.Clusters) > 0, true, "production rules", map[bool]string{true: "", false: "no cluster is marked prod: add clusters: in the config"}[prod > 0 || len(a.cfg.Clusters) > 0])
	}
	for _, d := range a.cfg.GitopsDirs() {
		check(fileExists(filepath.Join(d, ".git")), true, "gitops_repos "+tildePath(d), "")
	}
	for _, f := range a.cfg.Forwards {
		_, err := a.clusterContext(f.Cluster)
		check(err == nil, true, "favorite forward "+f.Name, map[bool]string{true: "", false: "cluster " + f.Cluster + " not found"}[err == nil])
	}

	section("agents")
	for _, s := range agentSkillDirs() {
		check(s.state == "current", true, "skill in "+tildePath(s.dir), map[string]string{"current": "", "outdated": "rex8s agents install", "missing": "rex8s agents install (optional)"}[s.state])
	}

	fmt.Println()
	if bad > 0 {
		return fmt.Errorf("%d problem(s) above", bad)
	}
	fmt.Println(tui.Ok.Render("all good"))
	return nil
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

var errNoConfig = errors.New("no config file")

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
	case !term.IsTerminal(int(os.Stdin.Fd())):
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
	if err := a.editConfig(func(doc string) string { return setYAMLKey(doc, "ask_agent", it.Key) }); err != nil {
		step(false, "could not save ask_agent: "+err.Error())
		return
	}
	step(true, "kask opens "+it.Key+" (saved; rex8s config set ask_agent to change)")
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
