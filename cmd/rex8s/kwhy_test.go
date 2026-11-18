package main

import (
	"strings"
	"testing"
)

func TestRedactEnv(t *testing.T) {
	in := `Containers:
  app:
    Image:  api:1
    Environment:
      DB_PASSWORD:  hunter2
      API_KEY:      <set to the key 'key' in secret 'api'>  Optional: false
      POD_IP:        (v1:status.podIP)
    Mounts:
      /data from data (rw)`
	out := redactEnv(in)
	if strings.Contains(out, "hunter2") || !strings.Contains(out, "DB_PASSWORD: <redacted>") ||
		!strings.Contains(out, "<set to the key 'key' in secret 'api'>") || !strings.Contains(out, "/data from data") {
		t.Fatalf("redactEnv:\n%s", out)
	}
}
