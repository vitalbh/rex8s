package kube

import "testing"

func TestParseNodes(t *testing.T) {
	data := []byte(`{"items":[{"metadata":{"name":"n1","labels":{"node-role.kubernetes.io/control-plane":""}},
	 "spec":{"unschedulable":true},"status":{"conditions":[{"type":"Ready","status":"True"},{"type":"DiskPressure","status":"True"}],
	 "nodeInfo":{"kubeletVersion":"v1.31.0"}}}]}`)
	ns, err := ParseNodes(data)
	if err != nil || len(ns) != 1 {
		t.Fatal(err, ns)
	}
	if n := ns[0]; !n.Ready || !n.Unschedulable || len(n.Roles) != 1 || n.Roles[0] != "control-plane" || len(n.Pressure) != 1 || n.Version != "v1.31.0" {
		t.Errorf("node: %+v", n)
	}
}
