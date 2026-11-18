package kube

import (
	"slices"
	"testing"
)

// Columns start where their titles start; a title or a cell may have a
// single space inside; a missing cell is "".
func TestParseTable(t *testing.T) {
	text := "" +
		"NAMESPACE   NAME             STATUS    ACCESS MODES   PORTS     AGE\n" +
		"api         redis-data       Bound     RWO            80, 443   200d\n" +
		"search      search-data      Pending                            2m\n" +
		"web         wébsite-données  Bound     RWX            80        5h3m\n"
	tb := ParseTable(text)
	if want := []string{"NAMESPACE", "NAME", "STATUS", "ACCESS MODES", "PORTS", "AGE"}; !slices.Equal(tb.Columns, want) {
		t.Fatalf("columns %q", tb.Columns)
	}
	want := [][]string{
		{"api", "redis-data", "Bound", "RWO", "80, 443", "200d"},
		{"search", "search-data", "Pending", "", "", "2m"},
		{"web", "wébsite-données", "Bound", "RWX", "80", "5h3m"},
	}
	for i, w := range want {
		if !slices.Equal(tb.Rows[i], w) {
			t.Errorf("row %d: %q, want %q", i, tb.Rows[i], w)
		}
	}
	if tb.Col("AGE") != 5 || tb.Col("NOPE") != -1 {
		t.Error("Col")
	}
	if got := ParseTable(""); len(got.Columns) != 0 {
		t.Error("an empty answer has no table")
	}
}

func TestAPIResources(t *testing.T) {
	text := "" +
		"NAME                       SHORTNAMES   APIVERSION                NAMESPACED   KIND\n" +
		"events                     ev           v1                        true         Event\n" +
		"secrets                                 v1                        true         Secret\n" +
		"customresourcedefinitions  crd,crds     apiextensions.k8s.io/v1   false        CustomResourceDefinition\n" +
		"events                     ev           events.k8s.io/v1          true         Event\n" +
		"ingresses                  ing          networking.k8s.io/v1      true         Ingress\n" +
		"widgets                    wd           example.com/v1            true         Widget\n"
	rs := ParseAPIResources(text)
	if len(rs) != 6 {
		t.Fatalf("%d resources", len(rs))
	}
	for name, want := range map[string]string{
		"ing": "ingresses.networking.k8s.io", "Ingress": "ingresses.networking.k8s.io", "crds": "customresourcedefinitions.apiextensions.k8s.io",
		"secret": "secrets", "ev": "events", "wd": "widgets.example.com", "widgets.example.com": "widgets.example.com",
	} {
		r, ok := FindAPIResource(rs, name)
		if !ok || r.Full() != want {
			t.Errorf("%s → %q (%v), want %q", name, r.Full(), ok, want)
		}
	}
	if r, _ := FindAPIResource(rs, "crd"); r.Namespaced {
		t.Error("CRDs are cluster-scoped")
	}
	if _, ok := FindAPIResource(rs, "gadgets"); ok {
		t.Error("an unknown kind was found")
	}
}

func TestParsePodOwnerAndNode(t *testing.T) {
	ps, err := ParsePods([]byte(`{"items":[
	 {"metadata":{"name":"web-7d9f6c5b8-x2k4p","namespace":"web","labels":{"pod-template-hash":"7d9f6c5b8"},
	  "ownerReferences":[{"kind":"Other","name":"x"},{"kind":"ReplicaSet","name":"web-7d9f6c5b8","controller":true}]},
	  "spec":{"nodeName":"node-a1","containers":[{"name":"app"}]},"status":{}},
	 {"metadata":{"name":"bare","namespace":"web"},"spec":{"containers":[{"name":"app"}]},"status":{}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].Owner != (OwnerRef{"ReplicaSet", "web-7d9f6c5b8"}) || ps[0].Node != "node-a1" {
		t.Fatalf("owner %+v node %q", ps[0].Owner, ps[0].Node)
	}
	if ps[1].Owner.Kind != "" {
		t.Fatalf("a bare pod has an owner: %+v", ps[1].Owner)
	}
}

func TestParseServiceSelector(t *testing.T) {
	ss, err := ParseServices([]byte(`{"items":[{"metadata":{"name":"web","namespace":"web"},
	 "spec":{"selector":{"tier":"front","app":"web"},"ports":[{"port":80}]}}]}`))
	if err != nil || len(ss) != 1 || ss[0].Selector != "app=web,tier=front" {
		t.Fatalf("%+v %v", ss, err)
	}
}
