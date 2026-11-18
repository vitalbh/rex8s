package kube

import "testing"

func TestParseServicesTCPOnly(t *testing.T) {
	ss, err := ParseServices([]byte(`{"items":[
	 {"metadata":{"name":"web"},"spec":{"type":"ClusterIP","ports":[{"name":"http","port":80},{"name":"dns","port":53,"protocol":"UDP"}]}},
	 {"metadata":{"name":"udp-only"},"spec":{"ports":[{"port":53,"protocol":"UDP"}]}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || len(ss[0].Ports) != 1 || ss[0].Ports[0].Port != 80 || ss[0].PortsText() != "80/http" {
		t.Fatalf("%+v", ss)
	}
}
