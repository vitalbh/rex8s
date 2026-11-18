package kube

import "testing"

func TestPodBadge(t *testing.T) {
	ps, err := ParsePods([]byte(`{"items":[
	 {"metadata":{"name":"ok"},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running","containerStatuses":[{"ready":true}]}},
	 {"metadata":{"name":"crash"},"spec":{"containers":[{"name":"app"}]},"status":{"phase":"Running","containerStatuses":[{"ready":false,"restartCount":7,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].Badge() != "ok" || ps[1].Badge() != "FAIL" || ps[1].Restarts != 7 || ps[1].Status() != "CrashLoopBackOff" {
		t.Fatalf("%+v", ps)
	}
}
