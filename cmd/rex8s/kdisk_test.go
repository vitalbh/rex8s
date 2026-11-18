package main

import (
	"strings"
	"testing"
)

func TestStatsSummary(t *testing.T) {
	data := `{"node":{"fs":{"usedBytes":80,"capacityBytes":100}},"pods":[
	  {"podRef":{"name":"mysql-0","namespace":"db"},"volume":[
	    {"name":"data","usedBytes":90,"capacityBytes":100,"pvcRef":{"name":"data-mysql-0","namespace":"db"}},
	    {"name":"tmp","usedBytes":1,"capacityBytes":100}]}]}`
	ds := parseStatsSummary([]byte(data), "node-a")
	if len(ds) != 2 || ds[0].kind != "node" || ds[1].name != "db/data-mysql-0" || ds[1].pod != "mysql-0" {
		t.Fatalf("ds = %+v", ds)
	}
	// a local-path PVC reports the node's disk: folded under the node
	local := `{"node":{"fs":{"usedBytes":80,"capacityBytes":100}},"pods":[{"podRef":{"name":"solr-0","namespace":"s"},"volume":[
	  {"usedBytes":80,"capacityBytes":100,"pvcRef":{"name":"solr-data","namespace":"s"}}]}]}`
	if ls := parseStatsSummary([]byte(local), "n"); len(ls) != 1 || len(ls[0].shared) != 1 || ls[0].shared[0] != "s/solr-data" {
		t.Errorf("local-path not folded: %+v", ls)
	}
	out := renderDisks(ds, false, false)
	if !strings.Contains(out, "db/data-mysql-0") || !strings.Contains(out, "90%") {
		t.Errorf("render:\n%s", out)
	}
}
