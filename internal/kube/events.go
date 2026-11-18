package kube

import (
	"encoding/json"
	"time"
)

// Event is a core/v1 Event as `kubectl get events -o json` (and -w) prints it.
type Event struct {
	Metadata struct {
		UID       string `json:"uid"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	InvolvedObject struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"involvedObject"`
	Reason        string    `json:"reason"`
	Message       string    `json:"message"`
	Count         int       `json:"count"`
	LastTimestamp time.Time `json:"lastTimestamp"`
	EventTime     time.Time `json:"eventTime"`
	Series        *struct {
		Count            int       `json:"count"`
		LastObservedTime time.Time `json:"lastObservedTime"`
	} `json:"series"`
}

// When is the event's last occurrence: lastTimestamp, else eventTime, and the
// series' lastObservedTime when the event is part of a series.
func (e Event) When() time.Time {
	when := e.LastTimestamp
	if when.IsZero() {
		when = e.EventTime
	}
	if e.Series != nil && !e.Series.LastObservedTime.IsZero() {
		when = e.Series.LastObservedTime
	}
	return when
}

// ParseEvents parses `kubectl get events -o json`.
func ParseEvents(data []byte) ([]Event, error) {
	var list struct {
		Items []Event `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}
