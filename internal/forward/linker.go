package forward

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
)

// Linker makes the cluster URLs in log lines clickable from this machine.
// The text stays as the program wrote it; the link (OSC 8) points to a
// localhost port this process holds with a Lazy forward, so kubectl only
// runs once someone clicks.
//
//   - http://NAME.NS.svc[.CLUSTER-DOMAIN][:PORT]/path → svc/NAME in NS
//     (only NS = Namespace unless AllNamespaces)
//   - http://0.0.0.0:PORT, localhost, 127.0.0.1, [::] → the pod that logged it
//
// At most Max ports are held at once: the least recently used one is closed
// to make room. A nil *Linker leaves lines alone.
type Linker struct {
	Client        kube.Client
	Context       string
	Namespace     string // the stream's namespace ("": any)
	AllNamespaces bool   // link services of other namespaces too
	ClusterDomain string // "": cluster.local
	Max           int    // ports held at once (0: DefaultMaxLinks)

	mu    sync.Mutex
	links map[string]*link // "ns|target|port"
}

// DefaultMaxLinks is how many ports a Linker holds at once by default.
const DefaultMaxLinks = 64

type link struct {
	port int // 0: could not listen
	ln   net.Listener
	pf   *Lazy
	used time.Time
}

func (k *link) lastUse() time.Time {
	if k.pf != nil {
		if t := k.pf.LastUse(); t.After(k.used) {
			return t
		}
	}
	return k.used
}

func (k *link) close() {
	if k.ln != nil {
		k.ln.Close()
	}
	if k.pf != nil {
		k.pf.Stop()
	}
}

var (
	// the whole host is taken, then checked in Go (RE2 has no lookahead)
	urlRe      = regexp.MustCompile(`\b(https?)://([a-z0-9](?:[-a-z0-9.]*[a-z0-9])?)(?::(\d{1,5}))?([^\s"'<>\x60\x1b]*)`)
	localURLRe = regexp.MustCompile(`\b(https?)://(?:0\.0\.0\.0|127\.0\.0\.1|localhost|\[::\]):(\d{1,5})([^\s"'<>\x60\x1b]*)`)
	// kubectl logs --prefix: "[pod/NAME/CONTAINER] "
	podPrefixRe = regexp.MustCompile(`^\[pod/([^/\]]+)/([^\]]*)\] `)
	dnsLabelRe  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

// svcHost splits NAME.NS.svc or NAME.NS.svc.DOMAIN; ok is false for any
// other host (a.b.svcs.example.com, a.b.svc.other.domain).
func svcHost(host, domain string) (name, ns string, ok bool) {
	parts := strings.SplitN(host, ".", 4)
	if len(parts) < 3 || parts[2] != "svc" || !dnsLabelRe.MatchString(parts[0]) || !dnsLabelRe.MatchString(parts[1]) {
		return "", "", false
	}
	if len(parts) == 4 && parts[3] != domain {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// local returns the localhost port for target:port, listening on first use.
func (l *Linker) local(ns, target string, port int, kind Kind) int {
	key := ns + "|" + target + "|" + strconv.Itoa(port)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.links == nil {
		l.links = map[string]*link{}
	}
	if k, ok := l.links[key]; ok {
		k.used = time.Now()
		return k.port
	}
	max := l.Max
	if max <= 0 {
		max = DefaultMaxLinks
	}
	for len(l.links) >= max {
		l.evict()
	}
	k := &link{used: time.Now()}
	l.links[key] = k
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	k.ln, k.port = ln, ln.Addr().(*net.TCPAddr).Port
	k.pf = &Lazy{Client: l.Client, Context: l.Context, Namespace: ns, Target: target, Remote: port, Kind: kind}
	go k.pf.Serve(ln)
	return k.port
}

// evict closes the least recently used port. l.mu is held.
func (l *Linker) evict() {
	var oldKey string
	var oldest time.Time
	for key, k := range l.links {
		if t := k.lastUse(); oldKey == "" || t.Before(oldest) {
			oldKey, oldest = key, t
		}
	}
	l.links[oldKey].close()
	delete(l.links, oldKey)
}

// Prune closes the ports of pods for which alive returns false (they are
// gone: a link to them can only fail).
func (l *Linker) Prune(alive func(pod string) bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, k := range l.links {
		parts := strings.SplitN(key, "|", 3)
		if pod, ok := strings.CutPrefix(parts[1], "pod/"); ok && !alive(pod) {
			k.close()
			delete(l.links, key)
		}
	}
}

// Close closes every port.
func (l *Linker) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for key, k := range l.links {
		k.close()
		delete(l.links, key)
	}
}

// Held is how many ports are open now.
func (l *Linker) Held() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, k := range l.links {
		if k.ln != nil {
			n++
		}
	}
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

// OSC8 wraps text in a terminal hyperlink to url.
func OSC8(url, text string) string { return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\" }

func schemeKind(scheme string) Kind {
	if scheme == "https" {
		return KindHTTPS
	}
	return KindHTTP
}

// Line rewrites one log line; pod is the pod that wrote it, or "" to take it
// from kubectl's --prefix.
func (l *Linker) Line(s, pod string) string {
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
			local := l.local(l.Namespace, "pod/"+pod, port, schemeKind(scheme))
			if local == 0 {
				return u
			}
			return OSC8(fmt.Sprintf("%s://localhost:%d%s", scheme, local, path), strings.TrimSuffix(u, rest)) + rest
		})
	}
	domain := l.ClusterDomain
	if domain == "" {
		domain = "cluster.local"
	}
	s = urlRe.ReplaceAllStringFunc(s, func(u string) string {
		m := urlRe.FindStringSubmatch(u)
		scheme, host, path := m[1], m[2], m[4]
		name, ns, ok := svcHost(host, domain)
		if !ok || (!l.AllNamespaces && l.Namespace != "" && ns != l.Namespace) {
			return u
		}
		port := 80
		if scheme == "https" {
			port = 443
		}
		if m[3] != "" {
			port, _ = strconv.Atoi(m[3])
		}
		path, rest := trimURLTail(path)
		local := l.local(ns, "svc/"+name, port, schemeKind(scheme))
		if local == 0 {
			return u
		}
		return OSC8(fmt.Sprintf("%s://localhost:%d%s", scheme, local, path), strings.TrimSuffix(u, rest)) + rest
	})
	return s
}
