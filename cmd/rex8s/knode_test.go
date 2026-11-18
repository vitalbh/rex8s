package main

import (
	"strings"
	"testing"

	"github.com/vitalbh/rex8s/internal/kube"
)

func TestParseNodes(t *testing.T) {
	in := `{"items":[
	  {"metadata":{"name":"b","labels":{"node-role.kubernetes.io/worker":""}},"status":{"conditions":[{"type":"Ready","status":"True"}],"nodeInfo":{"kubeletVersion":"v1.30.4"}}},
	  {"metadata":{"name":"c"},"status":{"conditions":[{"type":"Ready","status":"True"},{"type":"MemoryPressure","status":"True"}]}},
	  {"metadata":{"name":"a"},"spec":{"unschedulable":true},"status":{"conditions":[{"type":"Ready","status":"False"}]}}]}`
	ns, err := kube.ParseNodes([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	sortNodes(ns)
	got := []string{}
	for _, n := range ns {
		got = append(got, n.Name+":"+nodeBadge(n))
	}
	if strings.Join(got, " ") != "a:DOWN c:PRES b:" {
		t.Errorf("nodes = %v", got)
	}
	if ns[2].Roles[0] != "worker" || !strings.Contains(nodeHint(ns[2]), "v1.30.4") {
		t.Errorf("b = %+v", ns[2])
	}
}
