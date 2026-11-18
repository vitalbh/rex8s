package main

import (
	"strings"
	"testing"
)

func TestKreq(t *testing.T) {
	for in, want := range map[string]int64{"250m": 250, "2": 2000, "0.5": 500, "1500000n": 1, "": 0} {
		if got := parseCPU(in); got != want {
			t.Errorf("parseCPU(%q) = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[string]int64{"256Mi": 256 << 20, "1Gi": 1 << 30, "512M": 512e6, "1000": 1000} {
		if got := parseMem(in); got != want {
			t.Errorf("parseMem(%q) = %d, want %d", in, got, want)
		}
	}
	pods := `{"items":[
	 {"metadata":{"name":"api-7d9f6c5b8-4kq2x","namespace":"app"},"status":{"phase":"Running"},"spec":{"containers":[
	   {"name":"api","resources":{"requests":{"cpu":"1","memory":"1Gi"},"limits":{"memory":"1Gi"}}}]}},
	 {"metadata":{"name":"search-0","namespace":"app"},"status":{"phase":"Running"},"spec":{"containers":[
	   {"name":"search","resources":{"requests":{"memory":"2Gi"},"limits":{"memory":"2Gi"}}}]}},
	 {"metadata":{"name":"tool-x7k2p","namespace":"app"},"status":{"phase":"Running"},"spec":{"containers":[{"name":"tool"}]}}]}`
	top := "app api-7d9f6c5b8-4kq2x api 50m 100Mi\napp search-0 search 300m 1900Mi\napp tool-x7k2p tool 5m 20Mi\n"
	ws, err := workloadResources([]byte(pods), []byte(top), true, "")
	if err != nil || len(ws) != 3 {
		t.Fatalf("ws = %+v, %v", ws, err)
	}
	out := renderKreq(ws, "test")
	for _, want := range []string{"app/search", "OOM risk", "app/api", "app/tool"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q missing:\n%s", want, out)
		}
	}
}
