package main

import (
	"strings"
	"testing"
	"time"
)

func TestCron(t *testing.T) {
	at := func(s string) time.Time { v, _ := time.Parse("2006-01-02 15:04", s); return v }
	cases := []struct {
		expr, tz, from string
		want           []string
	}{
		{"*/15 * * * *", "", "2026-11-05 10:07", []string{"10:15", "10:30", "10:45"}},
		{"0 3 * * *", "", "2026-11-05 10:00", []string{"03:00"}},
		{"30 9 * * MON-FRI", "", "2026-11-06 10:00", []string{"09:30"}}, // Fri 10:00 → Mon 09:30
		{"@hourly", "", "2026-11-05 10:30", []string{"11:00", "12:00"}},
		{"0 6 * * *", "America/Sao_Paulo", "2026-11-05 00:00", []string{"09:00"}}, // 06:00 BRT = 09:00 UTC
	}
	for _, c := range cases {
		s, err := parseCron(c.expr, c.tz)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		got := s.runs(at(c.from), at(c.from).Add(72*time.Hour), len(c.want))
		var hm []string
		for _, g := range got {
			hm = append(hm, g.UTC().Format("15:04"))
		}
		if strings.Join(hm, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s from %s = %v, want %v", c.expr, c.from, hm, c.want)
		}
	}
	if s, _ := parseCron("30 9 * * MON-FRI", ""); s.runs(at("2026-11-06 10:00"), at("2026-11-09 23:59"), 1)[0].Weekday() != time.Monday {
		t.Error("weekday range")
	}
	for _, bad := range []string{"* * *", "61 * * * *", "*/0 * * * *"} {
		if _, err := parseCron(bad, ""); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
