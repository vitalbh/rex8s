package main

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// logLinker makes the cluster URLs in log lines clickable from this machine.
// The text stays as the program wrote it; the link (OSC 8) points to a
// localhost port this process holds with a lazyPF, so kubectl only runs
// once someone clicks. The ports live as long as the log pane.
//
//   - http://NAME.NS.svc[.cluster.local][:PORT]/path → svc/NAME in NS
//   - http://0.0.0.0:PORT, localhost, 127.0.0.1, [::] → the pod that logged it
type logLinker struct {
	kubectl string
	env     []string
	ctx, ns string // the stream's cluster and namespace

	mu    sync.Mutex
	ports map[string]int // "ns|target|port" → local port; 0: could not listen
}

func (a *app) newLogLinker(ctx, ns string) *logLinker {
	k, err := a.kubectl()
	if err != nil {
		return nil
	}
	return &logLinker{kubectl: k, env: setEnv(os.Environ(), "KUBECONFIG", a.src), ctx: ctx, ns: ns,
		ports: map[string]int{}}
}

var (
	svcURLRe   = regexp.MustCompile(`\b(https?)://([a-z0-9](?:[-a-z0-9]*[a-z0-9])?)\.([a-z0-9](?:[-a-z0-9]*[a-z0-9])?)\.svc(?:\.cluster\.local)?(?::(\d{1,5}))?([^\s"'<>\x60\x1b]*)`)
	localURLRe = regexp.MustCompile(`\b(https?)://(?:0\.0\.0\.0|127\.0\.0\.1|localhost|\[::\]):(\d{1,5})([^\s"'<>\x60\x1b]*)`)
	// kubectl logs --prefix: "[pod/NAME/CONTAINER] "
	podPrefixRe = regexp.MustCompile(`^\[pod/([^/\]]+)/([^\]]*)\] `)
)

// local returns the localhost port for target:port, listening on first use.
func (l *logLinker) local(ns, target string, port int, kind portKind) int {
	key := ns + "|" + target + "|" + strconv.Itoa(port)
	l.mu.Lock()
	defer l.mu.Unlock()
	if p, ok := l.ports[key]; ok {
		return p
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		l.ports[key] = 0
		return 0
	}
	p := &lazyPF{kubectl: l.kubectl, env: l.env, ctx: l.ctx, ns: ns, target: target, remote: port, kind: kind,
		idle: pfIdle, startWait: 20 * time.Second, logf: func(string, ...any) {}, lastUse: time.Now()}
	go p.serve(ln)
	n := ln.Addr().(*net.TCPAddr).Port
	l.ports[key] = n
	return n
}

// trimURLTail drops the punctuation that ends a sentence, not the URL.
func trimURLTail(path string) (keep, rest string) {
	i := len(path)
	for i > 0 && strings.ContainsRune(".,;:!?)]}'", rune(path[i-1])) {
		i--
	}
	return path[:i], path[i:]
}

func osc8(url, text string) string { return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\" }

// line rewrites one log line; pod is the pod that wrote it, or "" to take it
// from kubectl's --prefix.
func (l *logLinker) line(s, pod string) string {
	if l == nil || !strings.Contains(s, "://") {
		return s
	}
	if pod == "" {
		if m := podPrefixRe.FindStringSubmatch(s); m != nil {
			pod = m[1]
		}
	}
	// the pod's own "listening on" first: the svc links below point to
	// localhost, which this pass must not see
	if pod != "" {
		s = localURLRe.ReplaceAllStringFunc(s, func(u string) string {
			m := localURLRe.FindStringSubmatch(u)
			scheme, path := m[1], m[3]
			port, _ := strconv.Atoi(m[2])
			path, rest := trimURLTail(path)
			kind := kindHTTP
			if scheme == "https" {
				kind = kindHTTPS
			}
			local := l.local(l.ns, "pod/"+pod, port, kind)
			if local == 0 {
				return u
			}
			return osc8(fmt.Sprintf("%s://localhost:%d%s", scheme, local, path), strings.TrimSuffix(u, rest)) + rest
		})
	}
	s = svcURLRe.ReplaceAllStringFunc(s, func(u string) string {
		m := svcURLRe.FindStringSubmatch(u)
		scheme, name, ns, path := m[1], m[2], m[3], m[5]
		port := 80
		if scheme == "https" {
			port = 443
		}
		if m[4] != "" {
			port, _ = strconv.Atoi(m[4])
		}
		path, rest := trimURLTail(path)
		kind := kindHTTP
		if scheme == "https" {
			kind = kindHTTPS
		}
		local := l.local(ns, "svc/"+name, port, kind)
		if local == 0 {
			return u
		}
		return osc8(fmt.Sprintf("%s://localhost:%d%s", scheme, local, path), strings.TrimSuffix(u, rest)) + rest
	})
	return s
}
