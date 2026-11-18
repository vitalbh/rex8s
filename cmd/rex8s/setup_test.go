package main

import (
	"testing"
)

func TestBrewStable(t *testing.T) {
	if got, ok := brewStable("/opt/homebrew/Cellar/rex8s/0.1.0/bin/rex8s", "rex8s"); !ok || got != "/opt/homebrew/opt/rex8s/bin/rex8s" {
		t.Errorf("brewStable = %q %v", got, ok)
	}
	if _, ok := brewStable("/Users/x/.config/rex/rex8s/bin/rex8s", "rex8s"); ok {
		t.Error("not a brew path")
	}
}
