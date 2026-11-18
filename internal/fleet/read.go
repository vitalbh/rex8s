package fleet

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/vitalbh/rex8s/internal/kube"
)

// Reader runs the same kubectl reads on many clusters. Keep one for as long
// as the command runs (a view that refreshes): it remembers which clusters
// failed, so they are not waited for on every refresh. The zero value works
// (no probe, default kube.Client).
type Reader struct {
	// Client runs the queries (Client.Get: its Timeout bounds each one).
	Client kube.Client
	// Probe, when set, is how long to wait for a TCP connection to the API
	// server before reading. A cluster behind a VPN that is down fails
	// within Probe instead of Client.Timeout. Clusters reached through a
	// proxy (proxy-url, HTTPS_PROXY) are not probed.
	Probe time.Duration

	mu    sync.Mutex
	state map[string]*failure // by Key(cluster)
}

// failure: a cluster that was unreachable or timed out, and when to try
// it again.
type failure struct {
	reason       string
	since, retry time.Time
	streak       int
}

// Backoff after a cluster was unreachable or timed out: the first retry
// waits backoffMin, doubling up to backoffMax.
const (
	backoffMin = 30 * time.Second
	backoffMax = 5 * time.Minute
)

// Skip is a cluster a read left out.
type Skip struct {
	Cluster kube.Context
	// Reason is why: "login expired (kauth SHORT)", "unreachable (HOST:PORT)",
	// "timeout", kubectl's last stderr line, or a parse error.
	Reason string
	// Expired: the login expired (kube.AuthExpired); Reason says to run kauth.
	Expired bool
	// Since and Retry are set when the cluster is backed off: it has failed
	// since Since and is not tried again before Retry (Reader.Retry clears
	// that).
	Since, Retry time.Time
}

// String is "SHORT: reason", plus "(since 14:02, next try 14:05)" when the
// cluster is backed off.
func (s Skip) String() string {
	out := s.Cluster.Short + ": " + s.Reason
	if !s.Retry.IsZero() {
		out += " (since " + s.Since.Local().Format("15:04") + ", next try " + s.Retry.Local().Format("15:04:05") + ")"
	}
	return out
}

// Result is what a read returned.
type Result[T any] struct {
	Rows    []kube.On[T] // cluster by cluster in the order given, each in parse order
	OK      int          // clusters that answered every query and parsed
	Skipped []Skip       // the others, in the order given
}

// Strings is the skipped clusters as "SHORT: reason" lines.
func (r Result[T]) Strings() []string {
	out := make([]string, len(r.Skipped))
	for i, s := range r.Skipped {
		out[i] = s.String()
	}
	return out
}

// Parse turns one cluster's query outputs (one per query, in order) into
// rows. An error skips the cluster.
type Parse[T any] func(c kube.Context, out [][]byte) ([]T, error)

// Read runs the queries (kubectl arguments after --context, e.g. {"get",
// "pods", "-A", "-o", "json"}) on every cluster at once, the queries of one
// cluster one after the other, and parses each cluster's answers.
func Read[T any](r *Reader, clusters []kube.Context, queries [][]string, parse Parse[T]) Result[T] {
	return ReadEach(r, clusters, queries, parse, nil)
}

// ReadEach is Read that also calls each as every cluster finishes, from
// that cluster's goroutine (calls may run at the same time): with its rows,
// or with skip set. A view can show clusters as they answer.
func ReadEach[T any](r *Reader, clusters []kube.Context, queries [][]string, parse Parse[T], each func(c kube.Context, rows []kube.On[T], skip *Skip)) Result[T] {
	type res struct {
		rows []kube.On[T]
		skip *Skip
	}
	results := make([]res, len(clusters))
	var wg sync.WaitGroup
	for i, c := range clusters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, skip := readOne(r, c, queries, parse)
			results[i] = res{rows, skip}
			if each != nil {
				each(c, rows, skip)
			}
		}()
	}
	wg.Wait()

	var out Result[T]
	for _, x := range results {
		if x.skip != nil {
			out.Skipped = append(out.Skipped, *x.skip)
			continue
		}
		out.OK++
		out.Rows = append(out.Rows, x.rows...)
	}
	return out
}

func readOne[T any](r *Reader, c kube.Context, queries [][]string, parse Parse[T]) ([]kube.On[T], *Skip) {
	if s := r.backedOff(c); s != nil {
		return nil, s
	}
	if err := r.probe(c); err != nil {
		return nil, r.fail(c, err.Error())
	}
	outs := make([][]byte, 0, len(queries))
	for _, q := range queries {
		q, optional := isOptional(q)
		out, err := r.Client.Get(c.Name, q...)
		switch {
		case err == nil:
			outs = append(outs, out)
			continue
		case optional: // a missing CRD, a forbidden kind: no rows, the cluster still counts
			outs = append(outs, nil)
			continue
		case errors.Is(err, kube.ErrTimeout):
			return nil, r.fail(c, err.Error())
		case kube.AuthExpired(err):
			return nil, &Skip{Cluster: c, Reason: "login expired (kauth " + c.Short + ")", Expired: true}
		}
		return nil, &Skip{Cluster: c, Reason: lastLine(err.Error())}
	}
	r.ok(c)
	vs, err := parse(c, outs)
	if err != nil {
		return nil, &Skip{Cluster: c, Reason: err.Error()}
	}
	rows := make([]kube.On[T], len(vs))
	for i, v := range vs {
		rows[i] = kube.On[T]{C: c, V: v}
	}
	return rows, nil
}

// optionalMark leads a query that may fail (see Optional).
const optionalMark = "\x00optional"

// Optional marks a query (kubectl arguments) best effort: when it fails —
// a CRD the cluster doesn't have, a kind the user may not list — the
// parse function gets nil for its output instead of the cluster being
// skipped. A timeout of an optional query doesn't back the cluster off.
func Optional(q ...string) []string { return append([]string{optionalMark}, q...) }

func isOptional(q []string) ([]string, bool) {
	if len(q) > 0 && q[0] == optionalMark {
		return q[1:], true
	}
	return q, false
}

// Key identifies a cluster for the backoff: its server (aliases share it).
func Key(c kube.Context) string {
	if c.Server != "" {
		return c.Server
	}
	return c.Name
}

// backedOff returns the Skip of a cluster still waiting for its retry.
func (r *Reader) backedOff(c kube.Context) *Skip {
	r.mu.Lock()
	defer r.mu.Unlock()
	f := r.state[Key(c)]
	if f == nil || !time.Now().Before(f.retry) {
		return nil
	}
	return &Skip{Cluster: c, Reason: f.reason, Since: f.since, Retry: f.retry}
}

// fail records that c was unreachable or timed out and backs it off.
func (r *Reader) fail(c kube.Context, reason string) *Skip {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil {
		r.state = map[string]*failure{}
	}
	now := time.Now()
	f := r.state[Key(c)]
	if f == nil {
		f = &failure{since: now}
		r.state[Key(c)] = f
	}
	wait := backoffMin << min(f.streak, 10)
	f.reason, f.retry, f.streak = reason, now.Add(min(wait, backoffMax)), f.streak+1
	return &Skip{Cluster: c, Reason: reason, Since: f.since, Retry: f.retry}
}

func (r *Reader) ok(c kube.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.state, Key(c))
}

// Retry forgets every backoff: the next read tries all clusters again (a
// manual refresh).
func (r *Reader) Retry() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = nil
}

// probe opens (and closes) a TCP connection to c's API server.
func (r *Reader) probe(c kube.Context) error {
	if r.Probe <= 0 || c.ProxyURL != "" || proxyFromEnv() {
		return nil
	}
	addr := serverAddr(c.Server)
	if addr == "" {
		return nil
	}
	conn, err := net.DialTimeout("tcp", addr, r.Probe)
	if err != nil {
		return errors.New("unreachable (" + addr + ")")
	}
	conn.Close()
	return nil
}

// serverAddr is host:port of an API server URL ("" when it is not one).
func serverAddr(server string) string {
	u, err := url.Parse(server)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		default:
			return ""
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// proxyFromEnv: kubectl would go through a proxy, so a direct dial says
// nothing.
func proxyFromEnv() bool {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
