package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vitalbh/rex8s/internal/tui"
)

// Each running port-forward (pftail) leaves a small file in
// ~/.cache/rex8s/pf/<pid>.json while it runs, so `kpf -l` and the Raycast
// menu bar can list them and stop them.

type pfEntry struct {
	PID       int       `json:"pid"`
	Context   string    `json:"context"`
	Short     string    `json:"short"`
	Prod      bool      `json:"prod"`
	Namespace string    `json:"namespace"`
	Service   string    `json:"service"`
	Local     int       `json:"local"`
	Remote    int       `json:"remote"`
	Started   time.Time `json:"started"`
}

func (e pfEntry) URL() string { return fmt.Sprintf("http://localhost:%d", e.Local) }

func (a *app) pfDir() string { return filepath.Join(a.cache, "pf") }

func (a *app) pfRegister(e pfEntry) (remove func()) {
	dir := a.pfDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return func() {}
	}
	path := filepath.Join(dir, strconv.Itoa(e.PID)+".json")
	data, _ := json.Marshal(e)
	if os.WriteFile(path, data, 0o600) != nil {
		return func() {}
	}
	return func() { os.Remove(path) }
}

// pfList returns the running port-forwards, oldest first; files of processes
// that are gone are removed.
func (a *app) pfList() []pfEntry {
	files, _ := filepath.Glob(filepath.Join(a.pfDir(), "*.json"))
	out := []pfEntry{}
	for _, f := range files {
		var e pfEntry
		data, err := os.ReadFile(f)
		if err != nil || json.Unmarshal(data, &e) != nil || e.PID <= 0 || !alive(e.PID) {
			os.Remove(f)
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out
}

func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// pfStop ends a port-forward: its pane closes by itself.
func (a *app) pfStop(pid int) error {
	for _, e := range a.pfList() {
		if e.PID == pid {
			return syscall.Kill(pid, syscall.SIGTERM)
		}
	}
	return fmt.Errorf("no port-forward with pid %d", pid)
}

// kpfList: the running port-forwards; enter opens, copies or stops one.
func (a *app) kpfList() error {
	es := a.pfList()
	if len(es) == 0 {
		fmt.Println(tui.Dim.Render("no port-forwards running · kpf to start one"))
		return nil
	}
	byKey := map[string]pfEntry{}
	items := make([]tui.Item, 0, len(es))
	cw, sw := 0, 0
	for _, e := range es {
		cw = max(cw, len(e.Short))
		sw = max(sw, len(e.Namespace)+1+len(e.Service)+1+len(strconv.Itoa(e.Remote)))
	}
	for _, e := range es {
		k := strconv.Itoa(e.PID)
		byKey[k] = e
		items = append(items, tui.Item{Key: k, Badge: prodBadgeBool(e.Prod),
			Label: fmt.Sprintf("%-*s  %-*s → :%d", cw, e.Short, sw, fmt.Sprintf("%s/%s:%d", e.Namespace, e.Service, e.Remote), e.Local),
			Hint:  "since " + age(e.Started)})
	}
	it, err := tui.Run(tui.Options{Title: fmt.Sprintf("Port-forwards (%d running)", len(es)), Items: items})
	if err != nil || it == nil {
		return err
	}
	e := byKey[it.Key]
	act, err := tui.Run(tui.Options{Title: fmt.Sprintf("%s/%s on %s → %s", e.Namespace, e.Service, e.Short, e.URL()), Items: []tui.Item{
		{Key: "open", Label: fmt.Sprintf("%-6s", "open"), Hint: "open " + e.URL() + " in the browser"},
		{Key: "copy", Label: fmt.Sprintf("%-6s", "copy"), Hint: "copy " + e.URL()},
		{Key: "save", Label: fmt.Sprintf("%-6s", "save"), Hint: "save as a favorite (kpf NAME starts it again, also in Raycast)"},
		{Key: "stop", Label: fmt.Sprintf("%-6s", "stop"), Hint: "stop it (its pane closes)"},
	}})
	if err != nil || act == nil {
		return err
	}
	switch act.Key {
	case "open":
		return exec.Command("open", e.URL()).Run()
	case "copy":
		cmd := exec.Command("pbcopy")
		cmd.Stdin = strings.NewReader(e.URL())
		if err := cmd.Run(); err != nil {
			return err
		}
		fmt.Println("copied " + e.URL())
	case "save":
		fmt.Fprint(os.Stderr, tui.Bold.Render("Favorite name")+tui.Dim.Render(" (enter: "+e.Service+")")+"\n  > ")
		name := ""
		if tty, err := os.Open("/dev/tty"); err == nil {
			line, _ := bufio.NewReader(tty).ReadString('\n')
			tty.Close()
			name = strings.TrimSpace(line)
		}
		saved, err := a.saveFavorite(e, name)
		if err != nil {
			return err
		}
		fmt.Printf("saved %s · %s\n", tui.Bold.Render(saved), tui.Dim.Render("kpf "+saved+" starts it and opens the browser"))
	case "stop":
		if err := a.pfStop(e.PID); err != nil {
			return err
		}
		fmt.Printf("stopped %s/%s on %s\n", e.Namespace, e.Service, e.Short)
	}
	return nil
}

func prodBadgeBool(prod bool) string {
	if prod {
		return "PROD"
	}
	return ""
}
