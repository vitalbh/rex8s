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
	"syscall"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

const pfIdle = 10 * time.Minute

// lazyStart holds a local port for svc:port in the background (pfbg) and
// prints how to use it. A forward already serving the same port is reused.
func (a *app) lazyStart(c kube.Context, ns, svc string, port svcPort) error {
	kind := detectKind(port)
	for _, e := range a.pfList() {
		if e.Context == c.Name && e.Namespace == ns && e.Service == svc && e.Remote == port.Port {
			fmt.Println(tui.Dim.Render("already forwarded"))
			a.printForward(e, kind)
			return nil
		}
	}
	local := freePort(port.Port)
	if err := os.MkdirAll(a.pfDir(), 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(a.pfDir(), strconv.Itoa(local)+".log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer logf.Close()
	cmd := exec.Command(filepath.Join(a.binDir, "rex8s"), "pfbg", c.Name, ns, svc,
		fmt.Sprintf("%d:%d", local, port.Port), string(kind))
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	for i := 0; i < 40; i++ { // registered once it listens
		for _, e := range a.pfList() {
			if e.PID == pid {
				a.printForward(e, kind)
				return nil
			}
		}
		if !alive(pid) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	data, _ := os.ReadFile(logPath)
	return errors.New("forward did not start: " + lastLogLine(string(data)))
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
	fmt.Println(tui.Dim.Render(fmt.Sprintf("  kubectl connects on first use, stops after %s idle · kpf -l to stop", pfIdle)))
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

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	go func() {
		<-sig
		p.stop()
		unregister()
		os.Remove(filepath.Join(a.pfDir(), strconv.Itoa(local)+".log"))
		os.Exit(0)
	}()
	return p.serve(l)
}
