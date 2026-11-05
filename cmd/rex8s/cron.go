package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// cronSpec is a parsed 5-field cron schedule (minute hour dom month dow),
// as Kubernetes CronJobs use it.
type cronSpec struct {
	min, hour, dom, month, dow [64]bool
	domAny, dowAny             bool
	loc                        *time.Location
}

var cronMacros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var cronNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// parseCron parses a schedule; tz is the CronJob's timeZone ("" = UTC).
// "CRON_TZ=…" / "TZ=…" prefixes are honored too.
func parseCron(expr, tz string) (*cronSpec, error) {
	expr = strings.TrimSpace(expr)
	for _, p := range []string{"CRON_TZ=", "TZ="} {
		if strings.HasPrefix(expr, p) {
			rest := strings.SplitN(strings.TrimPrefix(expr, p), " ", 2)
			if len(rest) == 2 {
				tz, expr = rest[0], strings.TrimSpace(rest[1])
			}
		}
	}
	if m, ok := cronMacros[strings.ToLower(expr)]; ok {
		expr = m
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("cron %q: want 5 fields", expr)
	}
	s := &cronSpec{loc: time.UTC}
	if tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return nil, fmt.Errorf("time zone %q: %v", tz, err)
		}
		s.loc = loc
	}
	fields := []struct {
		set      *[64]bool
		lo, hi   int
		anyField *bool
	}{{&s.min, 0, 59, nil}, {&s.hour, 0, 23, nil}, {&s.dom, 1, 31, &s.domAny}, {&s.month, 1, 12, nil}, {&s.dow, 0, 7, &s.dowAny}}
	for i, fd := range fields {
		if err := cronField(f[i], fd.lo, fd.hi, fd.set); err != nil {
			return nil, fmt.Errorf("cron %q: %v", expr, err)
		}
		if fd.anyField != nil {
			*fd.anyField = f[i] == "*" || f[i] == "?"
		}
	}
	if s.dow[7] { // 7 is Sunday too
		s.dow[0] = true
	}
	return s, nil
}

func cronField(field string, lo, hi int, set *[64]bool) error {
	for _, part := range strings.Split(field, ",") {
		step := 1
		if r, st, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(st)
			if err != nil || n <= 0 {
				return fmt.Errorf("bad step %q", part)
			}
			part, step = r, n
		}
		from, to := lo, hi
		switch {
		case part == "*" || part == "?":
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			var err1, err2 error
			from, err1 = cronValue(a)
			to, err2 = cronValue(b)
			if err1 != nil || err2 != nil {
				return fmt.Errorf("bad range %q", part)
			}
		default:
			v, err := cronValue(part)
			if err != nil {
				return err
			}
			from, to = v, v
			if step > 1 { // "5/15" = from 5 every 15
				to = hi
			}
		}
		if from < lo || to > hi || from > to {
			return fmt.Errorf("%q out of range %d-%d", part, lo, hi)
		}
		for v := from; v <= to; v += step {
			set[v] = true
		}
	}
	return nil
}

func cronValue(s string) (int, error) {
	if v, ok := cronNames[strings.ToLower(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", s)
	}
	return v, nil
}

func (s *cronSpec) matches(t time.Time) bool {
	t = t.In(s.loc)
	if !s.min[t.Minute()] || !s.hour[t.Hour()] || !s.month[int(t.Month())] {
		return false
	}
	dom, dow := s.dom[t.Day()], s.dow[int(t.Weekday())]
	switch {
	case s.domAny && s.dowAny:
		return true
	case s.domAny:
		return dow
	case s.dowAny:
		return dom
	}
	return dom || dow // both restricted: either matches (standard cron)
}

// runs lists the times in (from, until], minute by minute.
func (s *cronSpec) runs(from, until time.Time, limit int) []time.Time {
	var out []time.Time
	t := from.Truncate(time.Minute).Add(time.Minute)
	for ; !t.After(until) && len(out) < limit; t = t.Add(time.Minute) {
		if s.matches(t) {
			out = append(out, t)
		}
	}
	return out
}
