package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestCommandTable(t *testing.T) {
	seen := map[string]string{}
	claim := func(name, by string) {
		if prev, ok := seen[name]; ok && prev != by {
			t.Errorf("%q is both %s and %s", name, prev, by)
		}
		seen[name] = by
	}
	for _, c := range commands {
		if c.name == "" || c.run == nil {
			t.Errorf("%+v: no name or no run", c)
			continue
		}
		if c.short == "" {
			t.Errorf("%s: no help (short)", c.name)
		}
		if c.hidden != strings.HasPrefix(c.name, "_") {
			t.Errorf("%s: internal commands, and only they, start with _", c.name)
		}
		if c.hidden && c.wrapper != "" {
			t.Errorf("%s: hidden but installed as %s", c.name, c.wrapper)
		}
		if c.wrapper != "" && c.wrapper != "khelp" && c.group == "" { // khelp is the picker itself
			t.Errorf("%s: wrapper %s has no khelp row (group)", c.name, c.wrapper)
		}
		if c.group != "" && !slices.Contains(helpGroups, c.group) {
			t.Errorf("%s: unknown khelp group %q", c.name, c.group)
		}
		claim(c.name, c.name)
		for _, al := range c.aliases {
			claim(al, c.name)
		}
		if c.wrapper != "" {
			claim(c.wrapper, c.name)
		}
	}
	for _, r := range helpRows {
		if r.cmd == "" || r.usage == "" || r.short == "" {
			t.Errorf("khelp row without cmd, usage or help: %+v", r)
		}
	}
}

// Every k* command setup installs runs a command of the table, and every
// wrapper of the table is installed.
func TestWrappersComeFromTheTable(t *testing.T) {
	var want []string
	for _, c := range commands {
		if c.wrapper != "" {
			want = append(want, c.wrapper)
		}
	}
	want = append(want, guardedTools...)
	slices.Sort(want)
	if got := wrapperNames(); !slices.Equal(got, want) {
		t.Errorf("wrapperNames() = %v\nwant %v", got, want)
	}
	for w, sub := range wrapperOf {
		c := lookup(w)
		if c == nil || c.name != sub {
			t.Errorf("wrapper %s → %s: not the table's command", w, sub)
		}
		if got := commandArgs([]string{"/x/bin/" + w, "a"}); !slices.Equal(got, []string{sub, "a"}) {
			t.Errorf("run as %s: %q, want %s a", w, got, sub)
		}
	}
}

// Every k* command is in docs/commands.md; every other command a user sees
// in rex8s help is somewhere in the docs or the README.
func TestCommandsAreDocumented(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cmds := read("../../docs/commands.md")
	all := cmds + read("../../README.md")
	more, _ := filepath.Glob("../../docs/*.md")
	for _, p := range more {
		all += read(p)
	}
	for _, c := range commands {
		switch {
		case c.hidden:
		case c.wrapper != "":
			if !strings.Contains(cmds, "`"+c.wrapper) && !strings.Contains(cmds, "**`"+c.wrapper) {
				t.Errorf("%s is not in docs/commands.md", c.wrapper)
			}
		case !strings.Contains(all, "rex8s "+c.name):
			t.Errorf("rex8s %s is in no doc", c.name)
		}
	}
}

func TestLookupKeepsOldNames(t *testing.T) {
	// an older rex8s.lua and saved Rex layouts run the old names
	for name, want := range map[string]string{
		"k9s": "browser", "klogs": "logs", "kctx": "switch", "--help": "help",
		"logtail": "_logs", "wlogtail": "_wlogs", "exectail": "_exec", "nodetail": "_node",
		"whytail": "_why", "asktail": "_ask", "nettail": "_net", "askpod": "_askpod",
		"pfbg": "_pf", "pfopen": "_pfopen", "warnings": "_warnings", "fwd": "_fwd",
	} {
		if c := lookup(name); c == nil || c.name != want {
			t.Errorf("lookup(%q) = %v, want %s", name, c, want)
		}
	}
}

// rex/rex8s.lua runs rex8s subcommands: each must be in the table.
func TestLuaRunsKnownCommands(t *testing.T) {
	b, err := os.ReadFile("../../rex/rex8s.lua")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`K8S, "([^"]+)"`)
	found := re.FindAllStringSubmatch(string(b), -1)
	if len(found) < 5 {
		t.Fatalf("found only %d rex8s calls in rex8s.lua", len(found))
	}
	for _, m := range found {
		c := lookup(m[1])
		if c == nil {
			t.Errorf("rex8s.lua runs rex8s %s: no such command", m[1])
		} else if c.name != m[1] {
			t.Errorf("rex8s.lua runs the old name %s: use %s", m[1], c.name)
		}
	}
}
