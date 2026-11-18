package kube

import (
	"testing"
	"time"
)

func TestParseEventsWhen(t *testing.T) {
	evs, err := ParseEvents([]byte(`{"items":[
	 {"metadata":{"uid":"u1","namespace":"app"},"involvedObject":{"kind":"Pod","name":"api-1"},"reason":"BackOff","count":5,"lastTimestamp":"2026-11-04T10:00:00Z"},
	 {"metadata":{"uid":"u2"},"eventTime":"2026-11-04T11:00:00Z","series":{"count":3,"lastObservedTime":"2026-11-04T12:00:00Z"}},
	 {"metadata":{"uid":"u3"},"eventTime":"2026-11-04T13:00:00Z"}]}`))
	if err != nil || len(evs) != 3 {
		t.Fatal(err, evs)
	}
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	if e := evs[0]; !e.When().Equal(at("2026-11-04T10:00:00Z")) || e.InvolvedObject.Name != "api-1" || e.Count != 5 {
		t.Errorf("lastTimestamp: %+v", e)
	}
	if e := evs[1]; !e.When().Equal(at("2026-11-04T12:00:00Z")) || e.Series.Count != 3 {
		t.Errorf("series: %+v", e)
	}
	if !evs[2].When().Equal(at("2026-11-04T13:00:00Z")) {
		t.Errorf("eventTime: %+v", evs[2])
	}
}
