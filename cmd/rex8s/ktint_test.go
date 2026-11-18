package main

import (
	"testing"
)

func TestNormTint(t *testing.T) {
	for in, want := range map[string]string{"#1F1430": "#1f1430", "1f1430": "#1f1430", "off": "off"} {
		if got, err := normTint(in); err != nil || got != want {
			t.Errorf("normTint(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := normTint("red"); err == nil {
		t.Error("normTint(red) should fail")
	}
}
