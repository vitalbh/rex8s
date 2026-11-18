package guard

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
)

func TestUnlock(t *testing.T) {
	use := kube.Context{Name: "use", Short: "use", Prod: true}
	euw := kube.Context{Name: "euw", Short: "euw", Prod: true}

	f := &fake{tty: true}
	log := setup(t, f)
	if err := Unlock("use", 15*time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := ConfirmChange(nil, []kube.Context{use}, "kroll"); err != nil || f.asked {
		t.Fatalf("unlocked: err=%v asked=%v", err, f.asked)
	}
	if !strings.Contains(strings.Join(auditLines(t, log), "\n"), "unlocked\tuse\tkroll") {
		t.Errorf("audit: %q", auditLines(t, log))
	}
	// one cluster unlocked, the other not: asks
	if err := ConfirmChange(nil, []kube.Context{use, euw}, "kroll"); !errors.Is(err, ErrCancelled) || !f.asked {
		t.Fatalf("partly unlocked: err=%v asked=%v", err, f.asked)
	}

	// an agent goes through an unlocked cluster, logged as such
	f.env["REX8S_AGENT"] = "1"
	if err := ConfirmChange(nil, []kube.Context{use}, "krun"); err != nil {
		t.Fatalf("agent, unlocked: %v", err)
	}
	if !strings.Contains(strings.Join(auditLines(t, log), "\n"), "unlocked\tuse\tkrun (agent)") {
		t.Errorf("audit: %q", auditLines(t, log))
	}
	// but not through a locked one
	if err := ConfirmChange(nil, []kube.Context{euw}, "krun"); !errors.Is(err, ErrRefused) {
		t.Fatalf("agent, locked: %v", err)
	}
	delete(f.env, "REX8S_AGENT")

	// expiry
	start := now
	now = func() time.Time { return start().Add(16 * time.Minute) }
	if !UnlockedUntil("use").IsZero() || len(Unlocks()) != 0 {
		t.Error("the unlock should have expired")
	}
	now = start

	// every cluster, then locked again
	if err := Unlock(AllClusters, time.Hour); err != nil {
		t.Fatal(err)
	}
	if UnlockedUntil("euw").IsZero() || Unlocks()[AllClusters].IsZero() {
		t.Error("all: euw should be unlocked")
	}
	if err := Lock(AllClusters); err != nil {
		t.Fatal(err)
	}
	if !UnlockedUntil("euw").IsZero() {
		t.Error("locked again")
	}
	if err := Lock("use"); err != nil {
		t.Errorf("locking what isn't unlocked: %v", err)
	}
}
