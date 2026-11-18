package main

import (
	"testing"
	"time"
)

func TestShortDur(t *testing.T) {
	for d, want := range map[time.Duration]string{10 * time.Minute: "10m", 8 * time.Hour: "8h", 90 * time.Minute: "1h30m"} {
		if got := shortDur(d); got != want {
			t.Errorf("shortDur(%v) = %q, want %q", d, got, want)
		}
	}
}
