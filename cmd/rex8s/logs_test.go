package main

import (
	"testing"
)

func TestLogRef(t *testing.T) {
	for _, c := range []struct{ in, kind, name, title string }{
		{"web-7d9f", "", "web-7d9f", "use/web-7d9f"},
		{"job/migrate", "job", "migrate", "use/migrate (job)"},
	} {
		k, n := logRef(c.in)
		if k != c.kind || n != c.name || logTitle("use", k, n) != c.title {
			t.Errorf("%q: %q %q %q", c.in, k, n, logTitle("use", k, n))
		}
	}
}
