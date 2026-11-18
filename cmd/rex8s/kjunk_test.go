package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/fleet"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/testkube"
)

func TestJunk(t *testing.T) {
	now := time.Now()
	ts := func(ago time.Duration) string { return now.Add(-ago).UTC().Format(time.RFC3339) }
	pods := fmt.Sprintf(`{"items":[
	  {"metadata":{"name":"node-debugger-n1-abcde","namespace":"default","creationTimestamp":%q},"status":{"phase":"Succeeded"}},
	  {"metadata":{"name":"knet-123","namespace":"api","creationTimestamp":%q},"status":{"phase":"Running"}},
	  {"metadata":{"name":"web-1","namespace":"web","creationTimestamp":%q},"status":{"phase":"Failed","reason":"Evicted"}},
	  {"metadata":{"name":"web-2","namespace":"web","creationTimestamp":%q},"status":{"phase":"Failed","reason":"Evicted"}},
	  {"metadata":{"name":"job-x","namespace":"web","creationTimestamp":%q,"ownerReferences":[{"kind":"Job"}]},"status":{"phase":"Failed"}}]}`,
		ts(184*24*time.Hour), ts(10*time.Minute), ts(3*24*time.Hour), ts(2*time.Hour), ts(9*24*time.Hour))
	ps := podJunk([]byte(pods), now, true, true)
	got := []string{}
	for _, j := range ps {
		got = append(got, j.cat+":"+j.name)
	}
	if strings.Join(got, " ") != "debug:node-debugger-n1-abcde pod:web-1" {
		t.Errorf("pods = %v", got)
	}
	jobs := fmt.Sprintf(`{"items":[
	  {"metadata":{"name":"old","namespace":"a"},"status":{"completionTime":%q}},
	  {"metadata":{"name":"recent","namespace":"a"},"status":{"completionTime":%q}},
	  {"metadata":{"name":"cron-1","namespace":"a","ownerReferences":[{"kind":"CronJob"}]},"status":{"completionTime":%q}},
	  {"metadata":{"name":"ttl","namespace":"a"},"spec":{"ttlSecondsAfterFinished":60},"status":{"completionTime":%q}}]}`,
		ts(30*24*time.Hour), ts(24*time.Hour), ts(30*24*time.Hour), ts(30*24*time.Hour))
	if js := jobJunk([]byte(jobs), now); len(js) != 1 || js[0].name != "old" {
		t.Errorf("jobs = %+v", js)
	}
	pvs := `{"items":[{"metadata":{"name":"pv-1"},"spec":{"persistentVolumeReclaimPolicy":"Retain","capacity":{"storage":"10Gi"},"claimRef":{"name":"data","namespace":"db"}},"status":{"phase":"Released"}},
	  {"metadata":{"name":"pv-2"},"spec":{},"status":{"phase":"Bound"}}]}`
	if vs := pvJunk([]byte(pvs), now); len(vs) != 1 || !strings.Contains(vs[0].why, "Retain") {
		t.Errorf("pvs = %+v", vs)
	}
}

// Each category reads only what it needs; a cluster that cannot list PVs
// still shows its other leftovers.
func TestClusterJunk(t *testing.T) {
	old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	k := testkube.New(t).
		On("*", "get pods *", testkube.Reply{Stdout: `{"items":[{"metadata":{"name":"web-1","namespace":"web","creationTimestamp":"` + old + `"},"status":{"phase":"Failed","reason":"Evicted"}}]}`}).
		On("*", "get jobs *", testkube.Reply{Stdout: `{"items":[{"metadata":{"name":"migrate","namespace":"a"},"status":{"completionTime":"` + old + `"}}]}`}).
		On("use", "get pv *", testkube.Reply{Stdout: `{"items":[{"metadata":{"name":"pv-1"},"spec":{"persistentVolumeReclaimPolicy":"Retain"},"status":{"phase":"Released"}}]}`}).
		On("euw", "get pv *", testkube.Reply{Stderr: `Error from server (Forbidden): persistentvolumes is forbidden` + "\n", Exit: 1})
	cl := kube.Client{Kubectl: k.Path, Kubeconfig: k.Kubeconfig, Timeout: 10 * time.Second}
	clusters := []kube.Context{{Name: "use", Short: "use"}, {Name: "euw", Short: "euw"}}

	for _, c := range []struct {
		cats  map[string]bool
		reads []string // what each cluster reads
		want  string
	}{
		{nil, []string{"get pods", "get jobs", "get pv"}, "euw:pod:web-1 euw:job:migrate use:pod:web-1 use:job:migrate use:pv:pv-1"},
		{map[string]bool{"job": true}, []string{"get jobs"}, "euw:job:migrate use:job:migrate"},
		{map[string]bool{"pv": true}, []string{"get pv"}, "use:pv:pv-1"},
	} {
		before := len(k.Calls())
		res := fleet.Read(&fleet.Reader{Client: cl}, clusters, junkQueries(c.cats), func(ctx kube.Context, out [][]byte) ([]junk, error) {
			return clusterJunk(cl, ctx, c.cats, out), nil
		})
		var got []string
		for _, r := range res.Rows {
			got = append(got, r.V.c.Short+":"+r.V.cat+":"+r.V.name)
		}
		slices.Sort(got)
		want := strings.Fields(c.want)
		slices.Sort(want)
		if len(res.Skipped) != 0 || !slices.Equal(got, want) {
			t.Errorf("%v: got %v, want %v (skipped %v)", c.cats, got, want, res.Strings())
		}
		if calls := k.Calls()[before:]; len(calls) != len(c.reads)*len(clusters) {
			t.Errorf("%v: calls %q, want %v per cluster", c.cats, calls, c.reads)
		}
	}
}
