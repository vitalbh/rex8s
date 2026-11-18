package guard

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Unlocking: for a while, the production guard lets changes to a cluster
// through without typing its name — kunlock, like sudo's grace period, for
// an incident or a maintenance window. Agents working for you are let
// through too (no more REX8S_FORCE on every command), but only a person at a
// terminal can unlock (kunlock refuses agents and scripts). Each unlocked
// change is logged as "unlocked", "(agent)" when an agent ran it.
//
// One file per cluster short name in ~/.cache/rex8s/unlock/, holding the
// expiry (RFC 3339); "*" unlocks every cluster.

// AllClusters unlocks every production cluster.
const AllClusters = "*"

func unlockDir() string {
	p := AuditPath()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "unlock")
}

func unlockFile(short string) string {
	name := short
	if short == AllClusters {
		name = "_all"
	}
	return filepath.Join(unlockDir(), strings.ReplaceAll(name, "/", "_"))
}

// Unlock lets changes to the production cluster short (AllClusters: every
// one) through without asking for d.
func Unlock(short string, d time.Duration) error {
	if err := os.MkdirAll(unlockDir(), 0o700); err != nil {
		return err
	}
	audit(short, "unlock", "unlocked for "+d.String())
	return os.WriteFile(unlockFile(short), []byte(now().Add(d).Format(time.RFC3339)), 0o600)
}

// Lock ends the unlock of short (AllClusters: every unlock, each cluster's
// too).
func Lock(short string) error {
	if short == AllClusters {
		audit("*", "lock", "locked again")
		return os.RemoveAll(unlockDir())
	}
	audit(short, "lock", "locked again")
	err := os.Remove(unlockFile(short))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// UnlockedUntil is when the unlock of short ends (its own or every
// cluster's, the later), or the zero time when it is locked.
func UnlockedUntil(short string) time.Time {
	var until time.Time
	for _, f := range []string{unlockFile(short), unlockFile(AllClusters)} {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
		if err != nil || !t.After(now()) {
			_ = os.Remove(f) // expired or unreadable: gone
			continue
		}
		if t.After(until) {
			until = t
		}
	}
	return until
}

// Unlocks: every unlock in force, cluster short name → until.
func Unlocks() map[string]time.Time {
	out := map[string]time.Time{}
	entries, _ := os.ReadDir(unlockDir())
	for _, e := range entries {
		short := e.Name()
		if short == "_all" {
			short = AllClusters
		}
		data, err := os.ReadFile(filepath.Join(unlockDir(), e.Name()))
		if err != nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data))); err == nil && t.After(now()) {
			out[short] = t
		}
	}
	return out
}

// unlocked: every one of prod is unlocked.
func unlocked(prod []string) bool {
	if len(prod) == 0 {
		return false
	}
	for _, p := range prod {
		if UnlockedUntil(p).IsZero() {
			return false
		}
	}
	return true
}

// UnlockedNames: the sorted short names in Unlocks, for display.
func UnlockedNames() []string {
	var names []string
	for n := range Unlocks() {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
