// Package kwatch keeps lists of Kubernetes objects up to date with watches
// (client-go informers), so a view that shows them doesn't run kubectl on
// every refresh: the first read lists, and from then on only changes come
// in. It answers the same kubectl queries the views already make ("get pods
// -A -o json"), with the same JSON, so their parsers don't change.
//
// It also keeps kubectl's tables ("get ingresses -o wide -A", any kind) up
// to date, as the API server prints them (table.go).
//
// It reads the kubeconfig like kubectl (the same contexts, exec credential
// plugins, proxies). What it can't serve — another query, a context it
// can't build a client for (a legacy auth-provider) — it says so, and the
// caller runs kubectl as before.
package kwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/vitalbh/rex8s/internal/kube"
)

// kind is a resource kwatch serves.
type kind struct {
	gvr        schema.GroupVersionResource
	kind       string
	namespaced bool
}

func (k kind) apiVersion() string { return k.gvr.GroupVersion().String() }

var kinds = map[string]kind{
	"pods":         {schema.GroupVersionResource{Version: "v1", Resource: "pods"}, "Pod", true},
	"services":     {schema.GroupVersionResource{Version: "v1", Resource: "services"}, "Service", true},
	"nodes":        {schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, "Node", false},
	"events":       {schema.GroupVersionResource{Version: "v1", Resource: "events"}, "Event", true},
	"deployments":  {schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "Deployment", true},
	"statefulsets": {schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, "StatefulSet", true},
	"daemonsets":   {schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, "DaemonSet", true},
	"jobs":         {schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, "Job", true},
	"cronjobs":     {schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, "CronJob", true},
}

// query is a kubectl query kwatch can answer: the kinds of
// "get A,B -A -o json [--field-selector S]", or a table.
type query struct {
	kinds  []kind
	fields string
	table  *tableQuery
}

// parseQuery reads kubectl arguments; ok is false for anything but a JSON
// list, across every namespace, of the kinds above.
func parseQuery(args []string) (q query, ok bool) {
	if len(args) < 2 || args[0] != "get" {
		return q, false
	}
	for _, r := range strings.Split(args[1], ",") {
		k, ok := kinds[r]
		if !ok {
			return q, false
		}
		q.kinds = append(q.kinds, k)
	}
	all, asJSON := false, false
	for i := 2; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-A" || a == "--all-namespaces":
			all = true
		case a == "-o" && i+1 < len(args):
			asJSON = args[i+1] == "json"
			i++
		case a == "-ojson" || a == "-o=json" || a == "--output=json":
			asJSON = true
		case a == "--field-selector" && i+1 < len(args):
			q.fields = args[i+1]
			i++
		case strings.HasPrefix(a, "--field-selector="):
			q.fields = strings.TrimPrefix(a, "--field-selector=")
		default:
			return q, false
		}
	}
	if !asJSON {
		return q, false
	}
	for _, k := range q.kinds {
		if k.namespaced && !all {
			return q, false
		}
	}
	return q, true
}

// Hub holds the watches of a process (a kview): one per cluster and query,
// started by the first Serve and kept until Release, IdleAfter or Stop.
// The zero value is not usable: make one with New.
type Hub struct {
	// FirstWait bounds how long the first Serve of a query waits for its
	// first list; after that a list not there yet is an error until it is.
	FirstWait time.Duration
	// IdleAfter stops a watch nothing has served or asked to be notified
	// of for that long (a tab nobody looks at): it holds every object of
	// its kinds in memory and a connection to the API server. The next
	// Serve starts it again, and waits for its list as the first did.
	// 0: never.
	IdleAfter time.Duration

	kubeconfig string
	// newClient and newTables make a cluster's clients (tests replace them).
	newClient func(ctxName string) (dynamic.Interface, *health, error)
	newTables func(ctxName string) (*tableClient, error)
	now       func() time.Time

	connMu sync.Mutex
	conns  map[string]*conn

	mu       sync.Mutex
	clients  map[string]*client
	watches  map[string]*watcher
	unserved map[string]bool // tables a cluster doesn't have: kubectl's
	swept    time.Time
	ctx      context.Context
	cancel   context.CancelFunc
}

type client struct {
	dyn    dynamic.Interface
	tables *tableClient // nil: kubectl prints its tables
	health *health      // nil in tests
	err    error        // can't watch this cluster: kubectl reads it
}

// health is whether a cluster's API server can be reached: client-go
// retries a refused watch quietly, forever, so a cluster that went away
// would otherwise keep showing its last rows as if they were current.
// Every connection the client opens says how it went.
type health struct {
	mu    sync.Mutex
	err   error
	since time.Time
}

func (hl *health) dial(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := d.DialContext(ctx, network, addr)
		if ctx.Err() != nil { // stopped, not down
			return c, err
		}
		hl.mu.Lock()
		defer hl.mu.Unlock()
		switch {
		case err == nil:
			hl.err = nil
		case hl.err == nil:
			hl.err, hl.since = fmt.Errorf("unreachable (%s)", addr), time.Now()
		}
		return c, err
	}
}

// failing is the error once connections have failed for at least d.
func (hl *health) failing(d time.Duration) error {
	if hl == nil {
		return nil
	}
	hl.mu.Lock()
	defer hl.mu.Unlock()
	if hl.err != nil && time.Since(hl.since) >= d {
		return hl.err
	}
	return nil
}

var quietLogs sync.Once

// New is a Hub for the kubeconfig kubectl would use (a path, a list of
// paths like KUBECONFIG, or "" for the default). It silences client-go's
// logging, which would otherwise write over the terminal.
func New(kubeconfig string) *Hub {
	quietLogs.Do(func() { klog.SetLogger(logr.Discard()) })
	h := &Hub{FirstWait: 15 * time.Second, IdleAfter: 5 * time.Minute, kubeconfig: kubeconfig, now: time.Now,
		clients: map[string]*client{}, watches: map[string]*watcher{}, unserved: map[string]bool{}, conns: map[string]*conn{}}
	h.newClient, h.newTables = h.build, h.buildTables
	h.ctx, h.cancel = context.WithCancel(context.Background())
	return h
}

// Stop ends every watch.
func (h *Hub) Stop() { h.cancel() }

// Serve answers kubectl arguments (after --context) on cluster c with what
// its watch holds, in kubectl's JSON. ok is false when it can't — another
// kind of query, or a cluster it can't build a client for — and the caller
// should run kubectl. The first Serve of a query starts its watch and waits
// up to FirstWait for the first list.
func (h *Hub) Serve(c kube.Context, args []string) (out []byte, ok bool, err error) {
	key, w := h.watch(c.Name, args)
	if w == nil {
		return nil, false, nil
	}
	err = w.wait(h.FirstWait)
	if w.notServed() {
		h.forget(key, w)
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	return w.list()
}

// Watching is whether Serve(ctxName, args) answers from a watch — false
// when kubectl does (a cluster without a client, a table it doesn't have),
// or when nothing has asked for it yet. It starts nothing.
func (h *Hub) Watching(ctxName string, args []string) bool {
	h.mu.Lock()
	w := h.watches[watchKey(ctxName, args)]
	h.mu.Unlock()
	return w != nil && !w.notServed()
}

// Release stops sending on ch (see Notify), and stops the watches nobody
// else asked to be notified by: a tab that closed.
func (h *Hub) Release(ch chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, w := range h.watches {
		w.mu.Lock()
		had := w.subs[ch]
		delete(w.subs, ch)
		left := len(w.subs)
		w.mu.Unlock()
		if had && left == 0 {
			w.stop()
			delete(h.watches, key)
		}
	}
}

// forget stops w, a query kwatch turned out not to serve: kubectl answers
// it from now on.
func (h *Hub) forget(key string, w *watcher) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.watches[key] == w {
		w.stop()
		delete(h.watches, key)
		h.unserved[key] = true
	}
}

// sweep stops the watches idle for IdleAfter (looking every IdleAfter/5).
func (h *Hub) sweep(now time.Time) {
	if h.IdleAfter <= 0 || now.Sub(h.swept) < h.IdleAfter/5 {
		return
	}
	h.swept = now
	for key, w := range h.watches {
		if now.Sub(w.used) >= h.IdleAfter {
			w.stop()
			delete(h.watches, key)
		}
	}
}

func watchKey(ctxName string, args []string) string {
	return ctxName + "\x00" + strings.Join(args, "\x00")
}

// Notify sends on ch (without blocking: give it a buffer of one) whenever
// what Serve(c, args) would answer changes. Calling it again with the same
// channel does nothing.
func (h *Hub) Notify(c kube.Context, args []string, ch chan struct{}) {
	if _, w := h.watch(c.Name, args); w != nil {
		w.mu.Lock()
		if w.subs != nil { // nil: stopped meanwhile
			w.subs[ch] = true
		}
		w.mu.Unlock()
	}
}

// watch returns the watcher of a query on ctxName (and its key), starting
// it; nil when kwatch doesn't serve it.
func (h *Hub) watch(ctxName string, args []string) (string, *watcher) {
	q, ok := parseQuery(args)
	if !ok {
		t, ok := parseTableQuery(args)
		if !ok {
			return "", nil
		}
		q.table = &t
	}
	key := watchKey(ctxName, args)
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if w := h.watches[key]; w != nil {
		w.used = now
		h.sweep(now)
		return key, w
	}
	h.sweep(now)
	cl := h.clientLocked(ctxName)
	if cl.err != nil || h.unserved[key] || (q.table != nil && cl.tables == nil) {
		return "", nil
	}
	ctx, cancel := context.WithCancel(h.ctx)
	var w *watcher
	if q.table != nil {
		w = &watcher{q: q, started: time.Now(), subs: map[chan struct{}]bool{}}
		go w.runTable(ctx, cl.tables, *q.table)
	} else {
		w = newWatcher(ctx, cl.dyn, q)
	}
	w.health, w.cancel, w.used = cl.health, cancel, now
	h.watches[key] = w
	return key, w
}

// watcher is one query's informers (one per kind) on one cluster.
type watcher struct {
	q         query
	informers []cache.SharedIndexInformer
	started   time.Time
	health    *health
	cancel    context.CancelFunc // stops its informers
	used      time.Time          // the last Serve or Notify (under Hub.mu)

	mu      sync.Mutex
	waited  bool
	subs    map[chan struct{}]bool
	err     error // the last watch error that matters
	errAt   time.Time
	errRV   []string // each informer's resource version when it failed
	changes int      // events so far: a change after an error ends it

	res      tableResource // a table's resource, once found
	columns  []metav1.TableColumnDefinition
	unserved bool // a table kubectl prints instead
}

// failingFor is how long a watch must keep failing, after its first list,
// for Serve to report it rather than answer what it last had.
const failingFor = 10 * time.Second

func newWatcher(ctx context.Context, dyn dynamic.Interface, q query) *watcher {
	w := &watcher{q: q, started: time.Now(), subs: map[chan struct{}]bool{}}
	var infs []cache.SharedIndexInformer
	for _, k := range q.kinds {
		ri := dyn.Resource(k.gvr)
		fields := q.fields
		lw := &cache.ListWatch{
			ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
				o.FieldSelector = fields
				return ri.List(ctx, o)
			},
			WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
				o.FieldSelector = fields
				return ri.Watch(ctx, o)
			},
		}
		// with dyn: a client without streaming lists (tests' fake) says so
		inf := cache.NewSharedIndexInformerWithOptions(cache.ToListWatcherWithWatchListSemantics(lw, dyn),
			&unstructured.Unstructured{}, cache.SharedIndexInformerOptions{})
		_ = inf.SetTransform(slim(k))
		w.watchInformer(inf)
		infs = append(infs, inf)
		go inf.RunWithContext(ctx)
	}
	w.mu.Lock()
	w.informers = infs
	w.mu.Unlock()
	return w
}

// watchInformer makes inf's errors and changes the watcher's.
func (w *watcher) watchInformer(inf cache.SharedIndexInformer) {
	_ = inf.SetWatchErrorHandlerWithContext(func(_ context.Context, _ *cache.Reflector, err error) { w.failed(err) })
	changed := func() { w.changed() }
	_, _ = inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(any) { changed() },
		UpdateFunc: func(any, any) { changed() },
		DeleteFunc: func(any) { changed() },
	})
}

// infs is the watcher's informers: none yet while a table's resource is
// being found.
func (w *watcher) infs() []cache.SharedIndexInformer {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.informers
}

// slim drops what no view reads and costs the most memory (managed fields,
// kubectl's last-applied annotation), and sets kind and apiVersion, which
// a list's items don't carry.
func slim(k kind) cache.TransformFunc {
	return func(o any) (any, error) {
		u, ok := o.(*unstructured.Unstructured)
		if !ok {
			return o, nil
		}
		u.SetManagedFields(nil)
		if a := u.GetAnnotations(); a["kubectl.kubernetes.io/last-applied-configuration"] != "" {
			delete(a, "kubectl.kubernetes.io/last-applied-configuration")
			u.SetAnnotations(a)
		}
		u.SetKind(k.kind)
		u.SetAPIVersion(k.apiVersion())
		return u, nil
	}
}

func (w *watcher) synced() bool {
	infs := w.infs()
	if len(infs) == 0 {
		return false
	}
	for _, inf := range infs {
		if !inf.HasSynced() {
			return false
		}
	}
	return true
}

func (w *watcher) versions() []string {
	infs := w.infs()
	out := make([]string, len(infs))
	for i, inf := range infs {
		out[i] = inf.LastSyncResourceVersion()
	}
	return out
}

func (w *watcher) changed() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.changes++
	w.err = nil
	w.wake()
}

// wake sends on every subscriber, with w.mu held: once stop or Release
// returns, nothing sends on the channels it dropped (their owner may close
// them).
func (w *watcher) wake() {
	for ch := range w.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// stop ends the watcher's informers and drops its subscribers: an event
// still on its way wakes nobody.
func (w *watcher) stop() {
	w.cancel()
	w.mu.Lock()
	w.subs = nil
	w.mu.Unlock()
}

// failed records a watch error. The ones every long watch sees (the server
// closed it, its resource version expired) are not failures: the informer
// lists again by itself.
func (w *watcher) failed(err error) {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		apierrors.IsResourceExpired(err) || apierrors.IsGone(err) {
		return
	}
	rv := w.versions()
	w.mu.Lock()
	if w.err == nil {
		w.errAt = time.Now()
	}
	w.err, w.errRV = err, rv
	w.wake() // a view waiting on its first list, or showing old rows, says why
	w.mu.Unlock()
}

// wait returns nil once the first list is in. The first call waits for it
// (up to max, or until a watch error); later ones answer at once.
func (w *watcher) wait(max time.Duration) error {
	w.mu.Lock()
	first := !w.waited
	w.waited = true
	w.mu.Unlock()
	deadline := time.Now().Add(max)
	for {
		if w.notServed() {
			return nil
		}
		if w.synced() {
			return w.failing()
		}
		w.mu.Lock()
		err := w.err
		w.mu.Unlock()
		if err != nil {
			return describe(err)
		}
		if err := w.health.failing(0); err != nil {
			return err
		}
		if !first || time.Now().After(deadline) {
			return fmt.Errorf("%w: no answer in %s, still trying", kube.ErrTimeout, time.Since(w.started).Round(time.Second))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// failing is the watch's error once it has failed, with nothing coming in
// since, for failingFor: the rows it has are getting old.
func (w *watcher) failing() error {
	if err := w.health.failing(failingFor); err != nil {
		return err
	}
	w.mu.Lock()
	err, at, rv := w.err, w.errAt, w.errRV
	w.mu.Unlock()
	if err == nil || time.Since(at) < failingFor {
		return nil
	}
	now := w.versions()
	for i := range now {
		if i < len(rv) && now[i] != rv[i] { // listed again since: it recovered
			return nil
		}
	}
	return describe(err)
}

// list is the objects as kubectl's "-o json" prints a list of them.
func (w *watcher) list() ([]byte, bool, error) {
	if w.q.table != nil {
		return w.tableText()
	}
	items := []any{}
	for _, inf := range w.infs() {
		for _, o := range inf.GetStore().List() {
			if u, ok := o.(*unstructured.Unstructured); ok {
				items = append(items, u.Object)
			}
		}
	}
	out, err := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": items})
	return out, true, err
}

// describe turns a client error into what kubectl's would say, short:
// "Unauthorized" (a login to renew), "unreachable (HOST:PORT)", the API
// server's message.
func describe(err error) error {
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		if apierrors.IsUnauthorized(err) {
			return errors.New("Unauthorized")
		}
		return errors.New(st.Status().Message)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		var ne net.Error
		var oe *net.OpError
		if errors.As(ue.Err, &oe) || (errors.As(ue.Err, &ne) && ne.Timeout()) {
			if u, perr := url.Parse(ue.URL); perr == nil {
				return fmt.Errorf("unreachable (%s)", u.Host)
			}
		}
		return ue.Err
	}
	return err
}

// conn is what a cluster's clients are made of: one http.Client (one
// credential plugin, one dialer watching the connections) for both.
type conn struct {
	cfg    *rest.Config
	hc     *http.Client
	health *health
	err    error
}

// build makes ctxName's client from the kubeconfig, like kubectl would.
// A context it can't use the way kubectl does is an error: kubectl reads
// that cluster.
func (h *Hub) build(ctxName string) (dynamic.Interface, *health, error) {
	c := h.connect(ctxName)
	if c.err != nil {
		return nil, nil, c.err
	}
	dyn, err := dynamic.NewForConfigAndClient(c.cfg, c.hc)
	return dyn, c.health, err
}

// buildTables makes ctxName's table client.
func (h *Hub) buildTables(ctxName string) (*tableClient, error) {
	c := h.connect(ctxName)
	if c.err != nil {
		return nil, c.err
	}
	base, _, err := rest.DefaultServerUrlFor(c.cfg)
	if err != nil {
		return nil, err
	}
	return &tableClient{base: base, hc: c.hc}, nil
}

func (h *Hub) connect(ctxName string) *conn {
	h.connMu.Lock()
	defer h.connMu.Unlock()
	if c := h.conns[ctxName]; c != nil {
		return c
	}
	cfg, hc, hl, err := h.config(ctxName)
	c := &conn{cfg, hc, hl, err}
	h.conns[ctxName] = c
	return c
}

func (h *Hub) config(ctxName string) (*rest.Config, *http.Client, *health, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if h.kubeconfig != "" {
		rules = &clientcmd.ClientConfigLoadingRules{Precedence: filepath.SplitList(h.kubeconfig)}
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules,
		&clientcmd.ConfigOverrides{CurrentContext: ctxName}).ClientConfig()
	if err != nil {
		return nil, nil, nil, err
	}
	if cfg.AuthProvider != nil { // gcp, azure, oidc: their plugins aren't built in
		return nil, nil, nil, errors.New("auth-provider " + cfg.AuthProvider.Name)
	}
	cfg.UserAgent = "rex8s"
	cfg.WarningHandler = rest.NoWarnings{}
	cfg.QPS, cfg.Burst = 20, 40
	// a cluster behind a VPN that is down fails fast, as with the probe
	hl := &health{}
	cfg.Dial = hl.dial(&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second})
	var hc *http.Client
	if cfg.ExecProvider != nil {
		// a credential plugin never reads the terminal (kview owns it), and
		// what it prints doesn't land on the screen
		cfg.ExecProvider.StdinUnavailable = true
		cfg.ExecProvider.StdinUnavailableMessage = "rex8s reads this cluster in the background"
		hc, err = quietStderr(func() (*http.Client, error) { return rest.HTTPClientFor(cfg) })
	} else {
		hc, err = rest.HTTPClientFor(cfg)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	return cfg, hc, hl, nil
}

var stderrMu sync.Mutex

// quietStderr runs f with os.Stderr on /dev/null. client-go's exec
// credential plugin keeps os.Stderr as it is when its client is made, for
// the plugin's stderr; this is the only way to keep a plugin's messages
// off the terminal.
func quietStderr[T any](f func() (T, error)) (T, error) {
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return f()
	}
	stderrMu.Lock()
	defer stderrMu.Unlock()
	old := os.Stderr
	os.Stderr = null
	defer func() { os.Stderr = old }()
	return f()
}

// clientLocked is ctxName's client, made the first time (h.mu held).
func (h *Hub) clientLocked(ctxName string) *client {
	cl := h.clients[ctxName]
	if cl == nil {
		dyn, hl, err := h.newClient(ctxName)
		cl = &client{dyn: dyn, health: hl, err: err}
		if err == nil {
			cl.tables, _ = h.newTables(ctxName)
		}
		h.clients[ctxName] = cl
	}
	return cl
}
