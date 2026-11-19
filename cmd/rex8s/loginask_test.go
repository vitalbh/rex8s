package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
)

func TestOfferLoginOncePerCluster(t *testing.T) {
	t.Setenv("REX8S_DEMO", "")
	a := &app{cache: t.TempDir()}
	var mu sync.Mutex
	asked := map[string]int{}
	done := make(chan struct{}, 10)
	old := loginAsk
	loginAsk = func(c kube.Context) {
		mu.Lock()
		asked[c.Short]++
		mu.Unlock()
		done <- struct{}{}
	}
	t.Cleanup(func() { loginAsk = old })

	use, euw := kube.Context{Name: "use-ctx", Short: "use"}, kube.Context{Name: "euw-ctx", Short: "euw"}
	a.offerLogin(use)
	a.offerLogin(use) // a second view of the same cluster: no second question
	a.offerLogin(euw)
	for range 2 {
		<-done
	}
	time.Sleep(20 * time.Millisecond)
	if asked["use"] != 1 || asked["euw"] != 1 {
		t.Fatalf("asked %v, want once each", asked)
	}
	// after loginAskEvery it asks again
	old2 := time.Now().Add(-loginAskEvery - time.Minute)
	_ = os.Chtimes(filepath.Join(a.cache, "login-asked", "use"), old2, old2)
	a.offerLogin(use)
	<-done
	if asked["use"] != 2 {
		t.Fatalf("asked %v, want use twice", asked)
	}
}
