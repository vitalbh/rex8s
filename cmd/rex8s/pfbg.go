package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vitalbh/rex8s/internal/desktop"
	"github.com/vitalbh/rex8s/internal/forward"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/tui"
)

// lazyStart holds a local port for svc:port in the background (pfbg) and
// prints how to use it. A forward already serving the same port is reused.
func (a *app) lazyStart(c kube.Context, ns, svc string, port kube.ServicePort) error {
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
func (a *app) lazyEnsure(c kube.Context, ns, svc string, port kube.ServicePort, wantLocal int) (forward.Entry, bool, error) {
	return a.registry().Ensure(c, ns, svc, port, wantLocal)
}

// detectKind: what speaks on a service port (forward.DetectKind).
func detectKind(p kube.ServicePort) forward.Kind { return forward.DetectKind(p) }

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
	port := kube.ServicePort{Port: n, Protocol: "TCP"}
	// the port's name and appProtocol tell its kind better than its number
	if cl, err := a.client(a.src); err == nil {
		if out, err := cl.Get(c.Name, "get", "services", "-n", ns, "--field-selector", "metadata.name="+svc, "-o", "json"); err == nil {
			if ss, err := kube.ParseServices(out); err == nil && len(ss) == 1 {
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
	fmt.Println(kind.URL(e.Local))
	if kind.Web() && desktop.CanOpen() {
		return desktop.OpenURL(kind.URL(e.Local))
	}
	return nil
}

func (a *app) printForward(e forward.Entry, kind forward.Kind) {
	fmt.Printf("%s  %s → %s\n", tui.Bold.Render("svc/"+e.Service+":"+strconv.Itoa(e.Remote)),
		tui.Dim.Render(e.Short+"/"+e.Namespace), tui.Ok.Render(kind.URL(e.Local)))
	if cl := kind.Client(e.Local); cl != "" {
		fmt.Println("  " + cl)
	}
	if cv := kind.Caveat(); cv != "" {
		fmt.Println("  " + tui.Dim.Render("note: "+cv))
	}
	gone := ""
	if d := a.cfg.ForwardExpiry(); d > 0 {
		gone = fmt.Sprintf(", the forward goes away after %s unused", shortDur(d))
	}
	fmt.Println(tui.Dim.Render(fmt.Sprintf("  kubectl connects on first use and stops after %s idle%s · kpf -l to stop", shortDur(forward.DefaultIdle), gone)))
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

// pfbg CTX NS SVC LOCAL:REMOTE KIND: the background side of lazyStart. It
// serves the listener lazyEnsure handed it (or listens on LOCAL), registers
// itself, and runs until stopped or unused for forward_expire.
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
	cl, err := a.client(a.src)
	if err != nil {
		return err
	}
	l, err := forward.Inherited(local)
	if err != nil {
		return err
	}
	reg := a.registry()
	p := &forward.Lazy{Client: cl, Context: c.Name, Namespace: args[1], Target: "svc/" + args[2], Remote: remote,
		Kind: forward.Kind(args[4]), Logf: forward.LogTo(os.Stdout)}
	unregister, err := reg.Register(forward.Entry{PID: os.Getpid(), Context: c.Name, Short: c.Short, Prod: c.Prod,
		Namespace: p.Namespace, Service: args[2], Local: local, Remote: remote, Started: time.Now(),
		Kind: p.Kind, Lazy: true})
	if err != nil {
		return fmt.Errorf("registering the forward: %w", err)
	}
	p.Logf("listening on 127.0.0.1:%d for %s:%d (%s/%s)", local, p.Target, remote, c.Short, p.Namespace)

	quit := func() {
		p.Stop()
		unregister()
		os.Remove(reg.LogPath(local))
		os.Exit(0)
	}
	// unused for forward_expire (8h by default): gone by itself
	p.Expire, p.OnExpire = a.cfg.ForwardExpiry(), quit
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGINT)
	go func() {
		<-sig
		quit()
	}()
	return p.Serve(l)
}
