package forward

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
)

func TestRegistryList(t *testing.T) {
	r := Registry{Dir: t.TempDir()}
	keep, err := r.Register(Entry{PID: os.Getpid(), Service: "api", Local: 8080, Remote: 80, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	r.Register(Entry{PID: 999999, Service: "gone", Started: time.Now()}) // no such process
	es := r.List()
	if len(es) != 1 || es[0].Service != "api" || es[0].URL() != "http://localhost:8080" || !es[0].Locked {
		t.Fatalf("list = %+v", es)
	}
	keep()
	if es := r.List(); len(es) != 0 {
		t.Fatalf("after remove: %+v", es)
	}
	if tmps, _ := filepath.Glob(filepath.Join(r.Dir, ".pf-*")); len(tmps) != 0 {
		t.Errorf("temporary files left: %v", tmps)
	}
}

func write(t *testing.T, path string, e Entry) {
	t.Helper()
	data, _ := json.Marshal(e)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A file whose process is gone stays gone even when its PID now belongs to
// another process (B11): nobody holds its lock.
func TestRegistryPIDReuse(t *testing.T) {
	r := Registry{Dir: t.TempDir()}
	os.MkdirAll(r.Dir, 0o700)
	// this test's PID is alive, but this file was not locked by anyone
	write(t, r.path(os.Getpid()), Entry{PID: os.Getpid(), Service: "ghost", Locked: true})
	if es := r.List(); len(es) != 0 {
		t.Fatalf("ghost listed: %+v", es)
	}
	if _, err := os.Stat(r.path(os.Getpid())); !os.IsNotExist(err) {
		t.Error("ghost file kept")
	}
	if err := r.Stop(os.Getpid()); err == nil {
		t.Error("Stop would signal a process that is not a forward")
	}
	// an entry from before the locks: trusted when its PID lives
	write(t, r.path(os.Getpid()), Entry{PID: os.Getpid(), Service: "old"})
	if es := r.List(); len(es) != 1 {
		t.Errorf("old entry: %+v", es)
	}
}

// A file that can't be read may be one being written: removed only once old.
func TestRegistryUnreadable(t *testing.T) {
	r := Registry{Dir: t.TempDir()}
	path := filepath.Join(r.Dir, "123.json")
	os.WriteFile(path, nil, 0o600)
	r.List()
	if _, err := os.Stat(path); err != nil {
		t.Fatal("a fresh unreadable file was removed")
	}
	old := time.Now().Add(-time.Minute)
	os.Chtimes(path, old, old)
	r.List()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("an old unreadable file was kept")
	}
}

func TestRegistryGC(t *testing.T) {
	r := Registry{Dir: t.TempDir()}
	remove, _ := r.Register(Entry{PID: os.Getpid(), Local: 4100, Started: time.Now()})
	defer remove()
	old := time.Now().Add(-time.Hour)
	for _, f := range []string{"4100.log", "4200.log", "4300.log", ".pf-x.tmp"} {
		os.WriteFile(filepath.Join(r.Dir, f), []byte("x"), 0o600)
		if f != "4300.log" {
			os.Chtimes(filepath.Join(r.Dir, f), old, old)
		}
	}
	write(t, filepath.Join(r.Dir, "999999.json"), Entry{PID: 999999, Locked: true})
	got := r.GC()
	if strings.Join(got, " ") != "999999.json 4200.log .pf-x.tmp" {
		t.Errorf("removed %v", got)
	}
	for _, f := range []string{"4100.log", "4300.log"} { // a live forward's, a fresh one
		if _, err := os.Stat(filepath.Join(r.Dir, f)); err != nil {
			t.Errorf("%s removed", f)
		}
	}
}

// TestHelperPfbg is a fake `rex8s _pf CTX NS SVC LOCAL:REMOTE KIND`: it
// serves the listener Ensure handed it and registers itself.
func TestHelperPfbg(t *testing.T) {
	if os.Getenv("FAKE_PFBG_DIR") == "" {
		return
	}
	args := flag.Args()
	if os.Getenv("FAKE_PFBG_FAIL") != "" {
		fmt.Println("listening…")
		fmt.Println("error: no such cluster")
		os.Exit(1)
	}
	var local, remote int
	fmt.Sscanf(args[4], "%d:%d", &local, &remote)
	l, err := Inherited(local)
	if err != nil || l.Addr().(*net.TCPAddr).Port != local {
		fmt.Println("no inherited listener:", err)
		os.Exit(1)
	}
	if os.Getenv(ListenerEnv) != "" {
		fmt.Println("listener variable passed on")
		os.Exit(1)
	}
	r := Registry{Dir: os.Getenv("FAKE_PFBG_DIR")}
	if _, err := r.Register(Entry{PID: os.Getpid(), Context: args[1], Namespace: args[2], Service: args[3],
		Local: local, Remote: remote, Kind: Kind(args[5]), Lazy: true, Started: time.Now()}); err != nil {
		os.Exit(1)
	}
	for {
		c, err := l.Accept()
		if err != nil {
			os.Exit(0)
		}
		c.Write([]byte("hi\n"))
		c.Close()
	}
}

func fakeRegistry(t *testing.T, env ...string) Registry {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "rex8s")
	os.WriteFile(bin, []byte("#!/bin/sh\nexec \""+os.Args[0]+"\" -test.run='^TestHelperPfbg$' -- \"$@\"\n"), 0o755)
	t.Setenv("FAKE_PFBG_DIR", dir)
	for i := 0; i+1 < len(env); i += 2 {
		t.Setenv(env[i], env[i+1])
	}
	r := Registry{Dir: dir, Bin: bin}
	t.Cleanup(func() { // no _pf left running
		for _, e := range r.List() {
			syscall.Kill(e.PID, syscall.SIGKILL)
		}
	})
	return r
}

func TestEnsure(t *testing.T) {
	r := fakeRegistry(t)
	c := kube.Context{Name: "use-ctx", Short: "use"}
	port := kube.ServicePort{Name: "http", Port: 80}
	var wg sync.WaitGroup
	got := make([]Entry, 4)
	reused := make([]bool, 4)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if got[i], reused[i], err = r.Ensure(c, "monitoring", "grafana", port, 0); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	started := 0
	for i := range got {
		if got[i].PID != got[0].PID || got[i].Local != got[0].Local {
			t.Fatalf("different forwards: %+v", got)
		}
		if !reused[i] {
			started++
		}
	}
	if started != 1 || len(r.List()) != 1 {
		t.Fatalf("started %d forwards, %d listed", started, len(r.List()))
	}
	e := got[0]
	if e.Kind != KindHTTP || e.Service != "grafana" || e.Namespace != "monitoring" {
		t.Errorf("entry %+v", e)
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", e.Local), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3)
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	conn.Read(buf)
	conn.Close()
	if string(buf) != "hi\n" {
		t.Errorf("forward answered %q", buf)
	}
	if err := r.Stop(e.PID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stopped forward still listed", func() bool { return len(r.List()) == 0 })
}

func TestEnsureFailure(t *testing.T) {
	r := fakeRegistry(t, "FAKE_PFBG_FAIL", "1")
	_, _, err := r.Ensure(kube.Context{Name: "c"}, "ns", "svc", kube.ServicePort{Port: 5432}, 0)
	if err == nil || !strings.Contains(err.Error(), "no such cluster") {
		t.Fatalf("err = %v", err)
	}
}

func TestListen(t *testing.T) {
	l, err := Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	taken := l.Addr().(*net.TCPAddr).Port
	l2, err := Listen(taken) // busy: another port
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	if l2.Addr().(*net.TCPAddr).Port == taken {
		t.Fatal("same port twice")
	}
}
