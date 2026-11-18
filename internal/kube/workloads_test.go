package kube

import "testing"

func TestParseWorkloads(t *testing.T) {
	ws, err := ParseWorkloads([]byte(`{"items":[
	 {"kind":"Deployment","metadata":{"name":"api","namespace":"app","generation":5,
	   "labels":{"kustomize.toolkit.fluxcd.io/name":"apps","kustomize.toolkit.fluxcd.io/namespace":"flux-system"}},
	  "spec":{"replicas":2},"status":{"observedGeneration":5,"updatedReplicas":2,"availableReplicas":1,
	   "conditions":[{"type":"Progressing","status":"False","reason":"ProgressDeadlineExceeded","message":"timed out"}]}},
	 {"kind":"DaemonSet","metadata":{"name":"agent","namespace":"kube-system","generation":3},
	  "status":{"observedGeneration":3,"desiredNumberScheduled":4,"updatedNumberScheduled":4,"numberAvailable":4}},
	 {"kind":"StatefulSet","metadata":{"name":"db","namespace":"app","generation":2},"spec":{"replicas":1},
	  "status":{"observedGeneration":2,"updatedReplicas":1,"readyReplicas":1,"currentRevision":"db-1","updateRevision":"db-2"}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	api, ds, sts := ws[0], ws[1], ws[2]
	if !api.Failed || api.RolledOut() || api.KsName != "apps" || api.KsNamespace != "flux-system" {
		t.Errorf("deployment: %+v", api)
	}
	if !ds.RolledOut() || ds.InProgress() {
		t.Errorf("daemonset should be rolled out: %+v", ds)
	}
	if sts.RolledOut() || !sts.InProgress() {
		t.Errorf("statefulset with pending revision is mid-rollout: %+v", sts)
	}
}

func TestParseWorkloadsReadyAndTemplate(t *testing.T) {
	data := []byte(`{"items":[
	 {"kind":"Deployment","metadata":{"name":"api","namespace":"app"},"spec":{"replicas":3,"selector":{"matchLabels":{"app":"api"}},
	  "template":{"spec":{"containers":[{"image":"reg/api:1.2"}]}}},"status":{"readyReplicas":2}},
	 {"kind":"DaemonSet","metadata":{"name":"agent","namespace":"ops"},"spec":{"selector":{"matchLabels":{"b":"2","a":"1"}}},
	  "status":{"numberReady":4,"desiredNumberScheduled":5}}]}`)
	ws, err := ParseWorkloads(data)
	if err != nil || len(ws) != 2 {
		t.Fatal(err, ws)
	}
	if w := ws[0]; w.Ready != 2 || w.Desired != 3 || w.Selector != "app=api" || w.Containers[0].Image != "reg/api:1.2" {
		t.Errorf("deployment: %+v", w)
	}
	if w := ws[1]; w.Ready != 4 || w.Desired != 5 || w.Selector != "a=1,b=2" {
		t.Errorf("daemonset: %+v", w)
	}
}

func TestParseWorkloadSelectors(t *testing.T) {
	ws, err := ParseWorkloads([]byte(`{"items":[
	 {"kind":"Deployment","metadata":{"name":"nginx","namespace":"web"},"spec":{"selector":{"matchLabels":{"tier":"edge","app":"nginx"}}}},
	 {"kind":"DaemonSet","metadata":{"name":"agent","namespace":"ops"},"spec":{"selector":{}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if ws[0].Selector != "app=nginx,tier=edge" || ws[0].Kind != "Deployment" || ws[1].Selector != "" {
		t.Errorf("selectors: %+v", ws)
	}
}
