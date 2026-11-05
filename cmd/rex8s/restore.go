package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vitalbh/rex8s/internal/demo"
	"github.com/vitalbh/rex8s/internal/rex"
	"github.com/vitalbh/rex8s/internal/tui"
)

// Each k8s pane keeps a small record (cluster, namespace, tab or layout).
// Closing the pane deletes it; when the Rex server goes away (restart,
// reboot) the record stays behind, and `restore` reopens those tabs.

type paneRecord struct {
	Block   string    `json:"block"`
	Context string    `json:"context"`
	Short   string    `json:"short,omitempty"`
	NS      string    `json:"ns,omitempty"`
	Layout  bool      `json:"layout,omitempty"`
	Opened  time.Time `json:"opened"`
}

func stateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		h, _ := os.UserHomeDir()
		base = filepath.Join(h, ".local", "state")
	}
	return filepath.Join(base, "rex8s", "panes")
}

func recordPath(block string) string {
	return filepath.Join(stateDir(), strings.TrimPrefix(block, "block:")+".json")
}

func saveRecord(r paneRecord) {
	if r.Block == "" || demo.Active() { // demo tabs aren't worth restoring
		return
	}
	_ = os.MkdirAll(stateDir(), 0o700)
	data, _ := json.Marshal(r)
	_ = os.WriteFile(recordPath(r.Block), data, 0o600)
}

func loadRecord(block string) (paneRecord, bool) {
	var r paneRecord
	data, err := os.ReadFile(recordPath(block))
	if err != nil || json.Unmarshal(data, &r) != nil {
		return r, false
	}
	return r, true
}

// updateRecord follows kctx/kns in the current pane.
func updateRecord(ctx, short, ns string) {
	b := rex.Block()
	if r, ok := loadRecord(b); ok {
		r.Context, r.Short, r.NS = ctx, short, ns
		saveRecord(r)
	}
}

// forgetOnClose decides, when a pane's shell ended, whether the user closed it
// (delete the record) or Rex went away (keep it for restore).
func forgetOnClose(block string, hangup bool) {
	if block == "" {
		return
	}
	if !hangup { // the user typed exit
		os.Remove(recordPath(block))
		return
	}
	// Server up and the tab gone: the user closed it. Server unreachable: Rex is
	// going away, keep the record. The tab may take a moment to disappear.
	id := strings.TrimPrefix(block, "block:")
	for i := 0; i < 6; i++ {
		live, ok := rex.BlocksTimeout(time.Second)
		if !ok {
			return
		}
		if !live[id] {
			os.Remove(recordPath(block))
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// lostPanes returns records whose pane no longer exists (oldest first).
// Records older than 30 days are dropped.
func lostPanes() []paneRecord {
	live, ok := rex.Blocks()
	if !ok {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(stateDir(), "*.json"))
	var out []paneRecord
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".json")
		if live[id] {
			continue
		}
		r, ok := loadRecord(id)
		if !ok || time.Since(r.Opened) > 30*24*time.Hour {
			os.Remove(f)
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Opened.Before(out[j].Opened) })
	return out
}

// restore reopens the k8s tabs lost when Rex restarted.
func (a *app) restore() error {
	lost := lostPanes()
	if len(lost) == 0 {
		fmt.Println(tui.Dim.Render("nothing to restore"))
		return nil
	}
	for _, r := range lost {
		action := "k8s_tab"
		if r.Layout {
			action = "k8s_layout"
		}
		args := map[string]any{"ctx": r.Context}
		if r.NS != "" {
			args["ns"] = r.NS
		}
		where := r.Context
		if r.NS != "" {
			where += "/" + r.NS
		}
		if err := rex.Do(action, args); err != nil {
			fmt.Println(tui.Err.Render("  " + where + ": " + err.Error()))
			continue
		}
		os.Remove(recordPath(r.Block))
		fmt.Println(tui.Ok.Render("  restored ") + where)
	}
	return nil
}

func restoreLabel(lost []paneRecord) string {
	names := make([]string, 0, len(lost))
	for _, r := range lost {
		n := r.Short
		if n == "" {
			n = r.Context
		}
		if r.NS != "" {
			n += "/" + r.NS
		}
		names = append(names, n)
	}
	return fmt.Sprintf("Restore %d tab(s) from the last Rex session: %s", len(lost), strings.Join(names, ", "))
}
