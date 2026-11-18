package kube

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Pod is one pod from `kubectl get pods -o json`, reduced to what the pickers
// and views show.
type Pod struct {
	Namespace  string
	Name       string
	Phase      string
	Reason     string // e.g. CrashLoopBackOff
	LastReason string // why a container last terminated, e.g. OOMKilled
	Ready      int
	Total      int
	Restarts   int
	Created    time.Time
	Containers []string
	Labels     map[string]string
}

// ParsePods parses `kubectl get pods -o json`. Reason is the most telling
// one: Terminating, the pod's own reason, or the first waiting (or, while
// running, terminated) container reason.
func ParsePods(data []byte) ([]Pod, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string            `json:"name"`
				Namespace         string            `json:"namespace"`
				Labels            map[string]string `json:"labels"`
				CreationTimestamp time.Time         `json:"creationTimestamp"`
				DeletionTimestamp *string           `json:"deletionTimestamp"`
			} `json:"metadata"`
			Spec struct {
				Containers []struct {
					Name string `json:"name"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				Phase             string `json:"phase"`
				Reason            string `json:"reason"`
				ContainerStatuses []struct {
					Ready        bool `json:"ready"`
					RestartCount int  `json:"restartCount"`
					State        struct {
						Waiting *struct {
							Reason string `json:"reason"`
						} `json:"waiting"`
						Terminated *struct {
							Reason string `json:"reason"`
						} `json:"terminated"`
					} `json:"state"`
					LastState struct {
						Terminated *struct {
							Reason string `json:"reason"`
						} `json:"terminated"`
					} `json:"lastState"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]Pod, 0, len(list.Items))
	for _, it := range list.Items {
		p := Pod{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, Phase: it.Status.Phase, Reason: it.Status.Reason,
			Created: it.Metadata.CreationTimestamp, Total: len(it.Spec.Containers), Labels: it.Metadata.Labels}
		if it.Metadata.DeletionTimestamp != nil {
			p.Reason = "Terminating"
		}
		for _, c := range it.Spec.Containers {
			p.Containers = append(p.Containers, c.Name)
		}
		for _, cs := range it.Status.ContainerStatuses {
			if cs.Ready {
				p.Ready++
			}
			p.Restarts += cs.RestartCount
			if p.Reason == "" && cs.State.Waiting != nil {
				p.Reason = cs.State.Waiting.Reason
			}
			if p.Reason == "" && cs.State.Terminated != nil && it.Status.Phase == "Running" {
				p.Reason = cs.State.Terminated.Reason
			}
			if p.LastReason == "" && cs.LastState.Terminated != nil {
				p.LastReason = cs.LastState.Terminated.Reason
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// Status is what kubectl shows in STATUS: the reason when there is one,
// otherwise the phase.
func (p Pod) Status() string {
	if p.Reason != "" {
		return p.Reason
	}
	return p.Phase
}

// Badge is the picker badge: "FAIL", "WAIT", "ok" or "done".
func (p Pod) Badge() string {
	switch {
	case p.Phase == "Succeeded":
		return "done"
	case p.Phase == "Failed" || strings.Contains(p.Reason, "BackOff") || strings.Contains(p.Reason, "Err") || p.Reason == "OOMKilled":
		return "FAIL"
	case p.Ready < p.Total || p.Phase != "Running":
		return "WAIT"
	}
	return "ok"
}

// Age formats the time since t like kubectl: 5m, 3h, 2d ("?" for zero).
func Age(t time.Time) string {
	d := time.Since(t)
	switch {
	case t.IsZero():
		return "?"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
