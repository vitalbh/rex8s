package main

import (
	"strings"
	"testing"
)

func TestCleanSpec(t *testing.T) {
	in := `{"apiVersion":"apps/v1","kind":"Deployment",
	  "metadata":{"name":"api","uid":"x","resourceVersion":"9","generation":4,"creationTimestamp":"2026-01-01T00:00:00Z",
	    "managedFields":[{}],"annotations":{"deployment.kubernetes.io/revision":"7"},"labels":{"app":"api"}},
	  "spec":{"replicas":3,"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"now"}},
	    "spec":{"containers":[{"name":"api","image":"acme/api:1"}]}}},
	  "status":{"readyReplicas":3}}`
	out, err := cleanSpec([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, gone := range []string{"uid", "resourceVersion", "generation", "managedFields", "revision", "restartedAt", "status", "readyReplicas", "annotations"} {
		if strings.Contains(s, gone) {
			t.Errorf("%q still in:\n%s", gone, s)
		}
	}
	for _, kept := range []string{"replicas: 3", "image: acme/api:1", "app: api"} {
		if !strings.Contains(s, kept) {
			t.Errorf("%q missing from:\n%s", kept, s)
		}
	}
}

func TestUnifiedDiff(t *testing.T) {
	out, same, err := unifiedDiff("a", []byte("x: 1\n"), "b", []byte("x: 2\n"))
	if err != nil || same || !strings.Contains(out, "-x: 1") || !strings.Contains(out, "+x: 2") {
		t.Fatalf("diff = %q same=%v err=%v", out, same, err)
	}
	if _, same, err := unifiedDiff("a", []byte("x\n"), "b", []byte("x\n")); err != nil || !same {
		t.Fatalf("same=%v err=%v", same, err)
	}
}
