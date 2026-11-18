package kube

import (
	"encoding/json"
	"time"
)

// Job is a batch/v1 Job. State is "failed", "running" or "done".
type Job struct {
	Namespace, Name, Cron string
	Start, End            time.Time
	Active, Failed        int
	State                 string // failed, running, done
	Message               string
}

// CronJob is a batch/v1 CronJob. Its own status has no result: see the
// newest Job it owns.
type CronJob struct {
	Namespace, Name, Schedule string
	TimeZone                  string // spec.timeZone ("" = the controller's, usually UTC)
	Suspend                   bool
	LastRun, LastOK           time.Time
}

// ParseJobs parses `kubectl get jobs -o json`. Cron is the owning CronJob.
func ParseJobs(data []byte) ([]Job, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string    `json:"name"`
				Namespace         string    `json:"namespace"`
				CreationTimestamp time.Time `json:"creationTimestamp"`
				OwnerReferences   []struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
			Status struct {
				StartTime      *time.Time `json:"startTime"`
				CompletionTime *time.Time `json:"completionTime"`
				Active         int        `json:"active"`
				Succeeded      int        `json:"succeeded"`
				Failed         int        `json:"failed"`
				Conditions     []struct {
					Type               string     `json:"type"`
					Status             string     `json:"status"`
					Message            string     `json:"message"`
					LastTransitionTime *time.Time `json:"lastTransitionTime"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(list.Items))
	for _, it := range list.Items {
		j := Job{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, Start: it.Metadata.CreationTimestamp,
			Active: it.Status.Active, Failed: it.Status.Failed, State: "running"}
		if it.Status.StartTime != nil {
			j.Start = *it.Status.StartTime
		}
		if it.Status.CompletionTime != nil {
			j.End = *it.Status.CompletionTime
		}
		for _, o := range it.Metadata.OwnerReferences {
			if o.Kind == "CronJob" {
				j.Cron = o.Name
			}
		}
		for _, c := range it.Status.Conditions {
			if c.Status != "True" {
				continue
			}
			switch c.Type {
			case "Failed":
				j.State, j.Message = "failed", c.Message
				if c.LastTransitionTime != nil && j.End.IsZero() {
					j.End = *c.LastTransitionTime
				}
			case "Complete":
				j.State = "done"
			}
		}
		if j.State == "running" && it.Status.Active == 0 && it.Status.Succeeded > 0 {
			j.State = "done"
		}
		out = append(out, j)
	}
	return out, nil
}

// ParseCronJobs parses `kubectl get cronjobs -o json`.
func ParseCronJobs(data []byte) ([]CronJob, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Schedule string `json:"schedule"`
				TimeZone string `json:"timeZone"`
				Suspend  bool   `json:"suspend"`
			} `json:"spec"`
			Status struct {
				LastScheduleTime   *time.Time `json:"lastScheduleTime"`
				LastSuccessfulTime *time.Time `json:"lastSuccessfulTime"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]CronJob, 0, len(list.Items))
	for _, it := range list.Items {
		c := CronJob{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, Schedule: it.Spec.Schedule, TimeZone: it.Spec.TimeZone, Suspend: it.Spec.Suspend}
		if t := it.Status.LastScheduleTime; t != nil {
			c.LastRun = *t
		}
		if t := it.Status.LastSuccessfulTime; t != nil {
			c.LastOK = *t
		}
		out = append(out, c)
	}
	return out, nil
}

// Badge is "FAIL", "WAIT" (running) or "done".
func (j Job) Badge() string {
	switch j.State {
	case "failed":
		return "FAIL"
	case "running":
		return "WAIT"
	}
	return "done"
}

// Took is how long the Job ran (so far, while running); "?" when unknown.
func (j Job) Took() string {
	switch {
	case j.Start.IsZero(), j.End.IsZero() && j.State == "failed":
		return "?"
	case j.End.IsZero():
		return time.Since(j.Start).Round(time.Second).String()
	}
	return j.End.Sub(j.Start).Round(time.Second).String()
}
