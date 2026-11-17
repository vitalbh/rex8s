package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

const pfIdle = 10 * time.Minute

// lazyStart holds a local port for svc:port in the background (pfbg) and
// prints how to use it. A forward already serving the same port is reused.
func (a *app) lazyStart(c kube.Context, ns, svc string, port svcPort) error {
	e, reused, err := a.lazyEnsure(c, ns, svc, port, 0)
	if err != nil {
		return err
	}
	if reused {
		fmt.Println(tui.Dim.Render("already forwarded"))
	}
	a.printForward(e, detectKind(port))
	return nil
}

// lazyEnsure returns the background forward of svc:port, starting one (on
// wantLocal when free, else near the service's port) when none runs.
func (a *app) lazyEnsure(c kube.Context, ns, svc string, port svcPort, wantLocal int) (pfEntry, bool, error) {
	kind := detectKind(port)
	for _, e := range a.pfList() {
		if e.Context == c.Name && e.Namespace == ns && e.Service == svc && e.Remote == port.Port {
			return e, true, nil
		}
	}
	if wantLocal == 0 {
		wantLocal = port.Port
	}
	local := freePort(wantLocal)
	if err := os.MkdirAll(a.pfDir(), 0o700); err != nil {
		return pfEntry{}, false, err
	}
	logPath := filepath.Join(a.pfDir(), strconv.Itoa(local)+".log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return pfEntry{}, false, err
	}
	defer logf.Close()
	cmd := exec.Command(filepath.Join(a.binDir, "rex8s"), "pfbg", c.Name, ns, svc,
		fmt.Sprintf("%d:%d", local, port.Port), string(kind))
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return pfEntry{}, false, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	for i := 0; i < 40; i++ { // registered once it listens
		for _, e := range a.pfList() {
			if e.PID == pid {
				return e, false, nil
			}
		}
		if !alive(pid) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	data, _ := os.ReadFile(logPath)
	return pfEntry{}, false, errors.New("forward did not start: " + lastLogLine(string(data)))
}

// pfopen CTX NS SVC PORT (Raycast's Port-forward): the background forward
// of that service port, opened in the browser when it speaks HTTP.
func (a *app) pfopen(args []string) error {
	if len(args) < 4 {
		return errors.New("pfopen CTX NS SVC PORT")
	}
	c, err := a.context(args[0])
	if err != nil {
		return err
	}
	ns, svc := args[1], args[2]
	n, err := strconv.Atoi(args[3])
	if err != nil {
		return fmt.Errorf("bad port %q", args[3])
	}
	port := svcPort{Port: n, Protocol: "TCP"}
	// the port's name and appProtocol tell its kind better than its number
	if cl, err := a.client(a.src); err == nil {
		if out, err := cl.Get(c.Name, "get", "services", "-n", ns, "--field-selector", "metadata.name="+svc, "-o", "json"); err == nil {
			if ss, err := parseServices(out); err == nil && len(ss) == 1 {
				for _, p := range ss[0].Ports {
					if p.Port == n {
						port = p
					}
				}
			}
		}
	}
	e, _, err := a.lazyEnsure(c, ns, svc, port, 0)
	if err != nil {
		return err
	}
	kind := detectKind(port)
	fmt.Println(kind.url(e.Local))
	if kind.web() {
		return exec.Command("open", kind.url(e.Local)).Run()
	}
	return nil
}

func (a *app) printForward(e pfEntry, kind portKind) {
	fmt.Printf("%s  %s → %s\n", tui.Bold.Render("svc/"+e.Service+":"+strconv.Itoa(e.Remote)),
		tui.Dim.Render(e.Short+"/"+e.Namespace), tui.Ok.Render(kind.url(e.Local)))
	if cl := kind.client(e.Local); cl != "" {
		fmt.Println("  " + cl)
	}
	if cv := kind.caveat(); cv != "" {
		fmt.Println("  " + tui.Dim.Render("note: "+cv))
	}
	gone := ""
	if d := a.cfg.ForwardExpiry(); d > 0 {
		gone = fmt.Sprintf(", the forward goes away after %s unused", shortDur(d))
	}
	fmt.Println(tui.Dim.Render(fmt.Sprintf("  kubectl connects on first use and stops after %s idle%s · kpf -l to stop", shortDur(pfIdle), gone)))
}

// shortDur: 10m, 8h, 1h30m — without time.Duration's trailing zeros.
func shortDur(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// pfbg CTX NS SVC LOCAL:REMOTE KIND: the background side of lazyStart.
func (a *app) pfbg(args []string) error {
	if len(args) < 5 {
		return errors.New("pfbg CTX NS SVC LOCAL:REMOTE KIND")
	}
	c, err := a.context(args[0])
	if err != nil {
		return err
	}
	var local, remote int
	if _, err := fmt.Sscanf(args[3], "%d:%d", &local, &remote); err != nil {
		return fmt.Errorf("bad mapping %q", args[3])
	}
	bin, err := a.kubectl()
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(local))
	if err != nil {
		return err
	}
	p := &lazyPF{kubectl: bin, env: setEnv(os.Environ(), "KUBECONFIG", a.src),
		ctx: c.Name, ns: args[1], target: "svc/" + args[2], remote: remote, kind: portKind(args[4]),
		idle: pfIdle, startWait: 20 * time.Second, logf: logTo(os.Stdout), lastUse: time.Now()}
	unregister := a.pfRegister(pfEntry{PID: os.Getpid(), Context: c.Name, Short: c.Short, Prod: c.Prod,
		Namespace: p.ns, Service: args[2], Local: local, Remote: remote, Started: time.Now(),
		Kind: p.kind, Lazy: true})
	p.logf("listening on 127.0.0.1:%d for %s:%d (%s/%s)", local, p.target, remote, c.Short, p.ns)

	quit := func() {
		p.stop()
		unregister()
		os.Remove(filepath.Join(a.pfDir(), strconv.Itoa(local)+".log"))
		os.Exit(0)
	}
	// unused for forward_expire (8h by default): gone by itself
	p.expire, p.onExpire = a.cfg.ForwardExpiry(), quit
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	go func() {
		<-sig
		quit()
	}()
	return p.serve(l)
}
