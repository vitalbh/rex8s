package config

import (
	"testing"
	"time"
)

func TestForwardExpiry(t *testing.T) {
	for v, want := range map[string]time.Duration{"": 8 * time.Hour, "off": 0, "2h": 2 * time.Hour, "junk": 8 * time.Hour} {
		if got := (&Config{ForwardExpire: v}).ForwardExpiry(); got != want {
			t.Errorf("ForwardExpiry(%q) = %v, want %v", v, got, want)
		}
	}
}
