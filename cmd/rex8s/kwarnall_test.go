package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestPrefixWriter(t *testing.T) {
	var out strings.Builder
	var mu sync.Mutex
	w := &prefixWriter{prefix: "use ", out: &out, mu: &mu}
	fmt.Fprint(w, "one\ntw")
	fmt.Fprint(w, "o\n")
	if got := out.String(); got != "use one\nuse two\n" {
		t.Fatalf("got %q", got)
	}
}
