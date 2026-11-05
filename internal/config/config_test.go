package config

import "testing"

func TestLookup(t *testing.T) {
	c := &Config{Clusters: []Cluster{
		{Match: "k8s.us-east.example.com", Name: "use", Prod: true},
		{Match: "legacy.example.com", Name: "legacy"},
	}}
	cases := []struct {
		server, ctx, short string
		prod               bool
	}{
		{"https://k8s.us-east.example.com:6443", "acme-prod-use", "use", true},
		{"https://k8s.us-east.example.com:6443", "Prod US East ", "use", true}, // alias inherits prod
		{"https://legacy.example.com:6443", "Legacy", "legacy", false},
		{"https://other:6443", "my cluster ", "my_cluster", false},
	}
	for _, tc := range cases {
		s, p := c.Lookup(tc.server, tc.ctx)
		if s != tc.short || p != tc.prod {
			t.Errorf("Lookup(%q,%q) = %q,%v; want %q,%v", tc.server, tc.ctx, s, p, tc.short, tc.prod)
		}
	}
}

func TestHiddenExact(t *testing.T) {
	c := &Config{Hide: []string{"Prod US East "}}
	if !c.Hidden("Prod US East ") || c.Hidden("Prod US East") {
		t.Fatal("Hidden must compare the exact name, including trailing space")
	}
}
