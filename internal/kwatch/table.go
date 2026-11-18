package kwatch

// kubectl's tables ("get ingresses -o wide -A", kview's ":" tabs) from a
// watch: the API server prints them (Accept: as=Table), with the columns
// kubectl would show — a CRD's printer columns too — and kwatch keeps each
// object's row up to date. Serve prints them as kubectl does, so the same
// parser reads either.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/vitalbh/rex8s/internal/kube"
)

// tableQuery is "get RESOURCE[.GROUP] -o wide [-A]".
type tableQuery struct {
	resource, group string
	all             bool
}

// parseTableQuery reads kubectl arguments; ok is false for anything but a
// wide table of one kind.
func parseTableQuery(args []string) (t tableQuery, ok bool) {
	if len(args) < 2 || args[0] != "get" || args[1] == "" || strings.ContainsAny(args[1], ",/") || strings.HasPrefix(args[1], "-") {
		return t, false
	}
	t.resource, t.group, _ = strings.Cut(args[1], ".")
	wide := false
	for i := 2; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-A" || a == "--all-namespaces":
			t.all = true
		case a == "-o" && i+1 < len(args):
			wide = args[i+1] == "wide"
			i++
		case a == "-owide" || a == "-o=wide" || a == "--output=wide":
			wide = true
		default:
			return t, false
		}
	}
	return t, wide
}

// tableResource is what discovery says a tableQuery names.
type tableResource struct {
	gvr        schema.GroupVersionResource
	namespaced bool
}

// errNotServed: the cluster has no such resource, or not one kwatch can
// watch as a table; kubectl answers (and says why).
var errNotServed = errors.New("not served")

// tableAccept asks the API server for a table instead of the objects.
const tableAccept = "application/json;as=Table;v=v1;g=meta.k8s.io,application/json"

// requestTimeout bounds a list or a discovery request (a watch runs on).
const requestTimeout = time.Minute

// tableClient reads tables from a cluster's API server over HTTP, through
// the http.Client client-go made (its credentials, proxy and dialer).
type tableClient struct {
	base *url.URL // the server, with its path (a Rancher proxy has one)
	hc   *http.Client
}

func (tc *tableClient) url(path []string, params url.Values) *url.URL {
	u := tc.base.JoinPath(path...)
	u.RawQuery = params.Encode()
	return u
}

// get answers a GET, or the API server's error as client-go would make it.
func (tc *tableClient) get(ctx context.Context, u *url.URL, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	resp, err := tc.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, statusError(resp.StatusCode, body)
	}
	return resp, nil
}

func statusError(code int, body []byte) error {
	var st metav1.Status
	if json.Unmarshal(body, &st) == nil && st.Kind == "Status" {
		return &apierrors.StatusError{ErrStatus: st}
	}
	return apierrors.NewGenericServerResponse(code, "get", schema.GroupResource{}, "", strings.TrimSpace(string(body)), 0, false)
}

func (tc *tableClient) getJSON(ctx context.Context, path []string, into any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	resp, err := tc.get(ctx, tc.url(path, nil), "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(into)
}

// resolve finds the resource on this cluster, like kubectl: its group's
// preferred version first, by name, singular or short name.
func (tc *tableClient) resolve(ctx context.Context, t tableQuery) (tableResource, error) {
	versions := []string{"v1"}
	if t.group != "" {
		var g metav1.APIGroup
		if err := tc.getJSON(ctx, []string{"apis", t.group}, &g); err != nil {
			if apierrors.IsNotFound(err) {
				return tableResource{}, errNotServed
			}
			return tableResource{}, err
		}
		versions = []string{g.PreferredVersion.Version}
		for _, v := range g.Versions {
			if !slices.Contains(versions, v.Version) {
				versions = append(versions, v.Version)
			}
		}
	}
	for _, v := range versions {
		path := []string{"apis", t.group, v}
		if t.group == "" {
			path = []string{"api", v}
		}
		var list metav1.APIResourceList
		if err := tc.getJSON(ctx, path, &list); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return tableResource{}, err
		}
		for _, r := range list.APIResources {
			if strings.Contains(r.Name, "/") || (r.Name != t.resource && r.SingularName != t.resource && !slices.Contains(r.ShortNames, t.resource)) {
				continue
			}
			if !slices.Contains(r.Verbs, "list") || !slices.Contains(r.Verbs, "watch") {
				return tableResource{}, errNotServed
			}
			return tableResource{schema.GroupVersionResource{Group: t.group, Version: v, Resource: r.Name}, r.Namespaced}, nil
		}
	}
	return tableResource{}, errNotServed
}

func (r tableResource) path() []string {
	if r.gvr.Group == "" {
		return []string{"api", r.gvr.Version, r.gvr.Resource}
	}
	return []string{"apis", r.gvr.Group, r.gvr.Version, r.gvr.Resource}
}

// noWatchList: the table client lists, then watches (a streaming list
// doesn't come as a table).
type noWatchList struct{}

func (noWatchList) IsWatchListSemanticsUnSupported() bool { return true }

// listWatch lists and watches r's table; each row becomes an object (its
// metadata, its cells) the informer keeps like any other. A list that
// doesn't come back as a table marks w not served.
func (tc *tableClient) listWatch(r tableResource, w *watcher) cache.ListerWatcher {
	params := func(o metav1.ListOptions) url.Values {
		p := url.Values{"includeObject": {"Metadata"}}
		for k, v := range map[string]string{"resourceVersion": o.ResourceVersion, "resourceVersionMatch": string(o.ResourceVersionMatch), "continue": o.Continue} {
			if v != "" {
				p.Set(k, v)
			}
		}
		if o.Limit > 0 {
			p.Set("limit", strconv.FormatInt(o.Limit, 10))
		}
		if o.TimeoutSeconds != nil {
			p.Set("timeoutSeconds", strconv.FormatInt(*o.TimeoutSeconds, 10))
		}
		return p
	}
	lw := &cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			ctx, cancel := context.WithTimeout(ctx, requestTimeout)
			defer cancel()
			resp, err := tc.get(ctx, tc.url(r.path(), params(o)), tableAccept)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			t, err := decodeTable(resp.Body)
			if err != nil {
				return nil, err
			}
			if t.Kind != "Table" { // an aggregated API that doesn't print tables
				w.notServe()
				return nil, errNotServed
			}
			w.setColumns(t.ColumnDefinitions)
			list := &unstructured.UnstructuredList{Object: map[string]any{}}
			list.SetResourceVersion(t.ResourceVersion)
			list.SetContinue(t.Continue)
			for _, row := range t.Rows {
				u, err := rowObject(row)
				if err != nil {
					return nil, err
				}
				list.Items = append(list.Items, *u)
			}
			return list, nil
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			p := params(o)
			p.Set("watch", "true")
			resp, err := tc.get(ctx, tc.url(r.path(), p), tableAccept)
			if err != nil {
				return nil, err
			}
			d := &tableDecoder{body: resp.Body, dec: json.NewDecoder(resp.Body)}
			d.dec.UseNumber()
			return watch.NewStreamWatcher(d, apierrors.NewClientErrorReporter(http.StatusInternalServerError, "GET", "ClientWatchDecoding")), nil
		},
	}
	return cache.ToListWatcherWithWatchListSemantics(lw, noWatchList{})
}

// decodeTable reads a table; numbers stay as the server wrote them.
func decodeTable(r io.Reader) (*metav1.Table, error) {
	dec := json.NewDecoder(r)
	dec.UseNumber()
	var t metav1.Table
	if err := dec.Decode(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

// rowObject is a row as the informer keeps it: the object's metadata (for
// its key, its resource version, its age), its cells, and when it came.
func rowObject(row metav1.TableRow) (*unstructured.Unstructured, error) {
	var o struct {
		Metadata metav1.ObjectMeta `json:"metadata"`
	}
	if err := json.Unmarshal(row.Object.Raw, &o); err != nil || o.Metadata.Name == "" {
		return nil, fmt.Errorf("a table row without its object: %v", err)
	}
	u := &unstructured.Unstructured{Object: map[string]any{"cells": cellsJSON(row.Cells), "received": time.Now().UnixNano()}}
	u.SetName(o.Metadata.Name)
	u.SetNamespace(o.Metadata.Namespace)
	u.SetUID(o.Metadata.UID)
	u.SetResourceVersion(o.Metadata.ResourceVersion)
	u.SetCreationTimestamp(o.Metadata.CreationTimestamp)
	return u, nil
}

// cellsJSON makes cells something unstructured can copy: what isn't a
// string, a number, a bool or null becomes the text kubectl would print.
func cellsJSON(cells []any) []any {
	out := make([]any, len(cells))
	for i, c := range cells {
		switch v := c.(type) {
		case nil, string, bool, json.Number:
			out[i] = v
		default:
			out[i] = fmt.Sprint(v)
		}
	}
	return out
}

// tableDecoder reads a table watch: each event's object is a table of the
// one row that changed.
type tableDecoder struct {
	body    io.ReadCloser
	dec     *json.Decoder
	pending []*unstructured.Unstructured
	typ     watch.EventType
}

func (d *tableDecoder) Decode() (watch.EventType, runtime.Object, error) {
	for len(d.pending) == 0 {
		var ev struct {
			Type   watch.EventType `json:"type"`
			Object json.RawMessage `json:"object"`
		}
		if err := d.dec.Decode(&ev); err != nil {
			return "", nil, err
		}
		switch ev.Type {
		case watch.Bookmark:
			continue
		case watch.Error:
			var st metav1.Status
			if err := json.Unmarshal(ev.Object, &st); err != nil {
				return "", nil, err
			}
			return watch.Error, &st, nil
		}
		t, err := decodeTable(bytes.NewReader(ev.Object))
		if err != nil {
			return "", nil, err
		}
		for _, row := range t.Rows {
			u, err := rowObject(row)
			if err != nil {
				return "", nil, err
			}
			d.pending = append(d.pending, u)
		}
		d.typ = ev.Type
	}
	u := d.pending[0]
	d.pending = d.pending[1:]
	return d.typ, u, nil
}

func (d *tableDecoder) Close() { d.body.Close() }

// runTable finds the resource, then watches its table; until the first
// works it retries, with the error for Serve to say.
func (w *watcher) runTable(ctx context.Context, tc *tableClient, t tableQuery) {
	for wait := time.Second; ; wait = min(2*wait, 30*time.Second) {
		r, err := tc.resolve(ctx, t)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, errNotServed) || (r.namespaced && !t.all): // one namespace: kubectl's
			w.notServe()
			return
		case err == nil:
			inf := cache.NewSharedIndexInformerWithOptions(tc.listWatch(r, w), &unstructured.Unstructured{}, cache.SharedIndexInformerOptions{})
			w.watchInformer(inf)
			w.mu.Lock()
			w.res = r
			w.informers = []cache.SharedIndexInformer{inf}
			w.mu.Unlock()
			inf.RunWithContext(ctx)
			return
		}
		w.failed(err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (w *watcher) setColumns(cols []metav1.TableColumnDefinition) {
	if len(cols) == 0 {
		return
	}
	w.mu.Lock()
	w.columns = cols
	w.mu.Unlock()
}

func (w *watcher) notServe() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.unserved = true
	w.wake() // a view waiting on it reads kubectl now
}

func (w *watcher) notServed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.unserved
}

// tableText is the rows as kubectl prints them with -o wide: NAMESPACE
// first across namespaces, every column the server gave (the wide ones
// too), titles in capitals, cells padded by three spaces. Ages count on
// from when each row came.
func (w *watcher) tableText() ([]byte, bool, error) {
	w.mu.Lock()
	cols, withNS := w.columns, w.res.namespaced
	w.mu.Unlock()
	var objs []*unstructured.Unstructured
	for _, inf := range w.infs() {
		for _, o := range inf.GetStore().List() {
			if u, ok := o.(*unstructured.Unstructured); ok {
				objs = append(objs, u)
			}
		}
	}
	sort.Slice(objs, func(i, j int) bool {
		a, b := objs[i], objs[j]
		if a.GetNamespace() != b.GetNamespace() {
			return a.GetNamespace() < b.GetNamespace()
		}
		return a.GetName() < b.GetName()
	})
	var head []string
	if withNS {
		head = append(head, "NAMESPACE")
	}
	for _, c := range cols {
		head = append(head, strings.ToUpper(c.Name))
	}
	rows := [][]string{head}
	now := time.Now()
	for _, o := range objs {
		var row []string
		if withNS {
			row = append(row, o.GetNamespace())
		}
		cells, _ := o.Object["cells"].([]any)
		for i, c := range cols {
			var cell any
			if i < len(cells) {
				cell = cells[i]
			}
			row = append(row, cellText(cell, c, o, now))
		}
		rows = append(rows, row)
	}
	return render(rows), true, nil
}

// cellText is a cell as kubectl prints it: nothing for null, a string up
// to its first line break. A date column (AGE, LAST SCHEDULE) is the age
// the server printed plus the time since; AGE from the object itself.
func cellText(cell any, col metav1.TableColumnDefinition, o *unstructured.Unstructured, now time.Time) string {
	if strings.EqualFold(col.Name, "age") {
		if ts := o.GetCreationTimestamp(); !ts.IsZero() {
			return duration.HumanDuration(now.Sub(ts.Time))
		}
	}
	if s, ok := cell.(string); ok && col.Type == "date" {
		if d, ok := kube.ParseAge(s); ok {
			received, _ := o.Object["received"].(int64)
			return duration.HumanDuration(d + now.Sub(time.Unix(0, received)))
		}
	}
	var s string
	switch v := cell.(type) {
	case nil:
		return ""
	case string:
		s = v
	default:
		s = fmt.Sprint(v)
	}
	if i := strings.IndexAny(s, "\f\n\r"); i >= 0 {
		s = s[:i] + "..."
	}
	return strings.ReplaceAll(s, "\t", " ")
}

// render aligns rows like kubectl's tabwriter: each column as wide as its
// widest cell, three spaces between.
func render(rows [][]string) []byte {
	var widths []int
	for _, r := range rows {
		for i, c := range r {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], utf8.RuneCountInString(c))
		}
	}
	var b bytes.Buffer
	for _, r := range rows {
		for i, c := range r {
			b.WriteString(c)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(c)+3))
			}
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}
