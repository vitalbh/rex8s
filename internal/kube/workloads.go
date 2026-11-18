package kube

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Workload is a Deployment, StatefulSet or DaemonSet. The Ks* fields are set
// when a Flux Kustomization applied it (kustomize.toolkit.fluxcd.io labels).
type Workload struct {
	Kind, Namespace, Name string
	Created               time.Time
	Selector              string      // spec.selector.matchLabels as "k=v,k=v", sorted; "" when it has none
	Containers            []Container // of the pod template

	Generation, Observed int64
	Desired, Updated     int
	// Ready is what kubectl shows as READY: readyReplicas (DaemonSet:
	// numberReady).
	Ready int
	// Available is what a rollout waits for: availableReplicas for a
	// Deployment, numberAvailable for a DaemonSet, readyReplicas for a
	// StatefulSet.
	Available             int
	Failed                bool   // Deployment: ProgressDeadlineExceeded
	Message               string // why it failed / what it waits for
	KsName, KsNamespace   string // owning Kustomization
	currentRev, updateRev string // StatefulSet revisions
}

// Container is a container of a workload's pod template.
type Container struct{ Name, Image string }

// Key identifies the workload within a cluster.
func (w Workload) Key() string { return w.Kind + "/" + w.Namespace + "/" + w.Name }

// RolledOut reports whether the controller has caught up with the latest spec
// and every replica runs it and is available.
func (w Workload) RolledOut() bool {
	if w.Observed < w.Generation || w.Updated < w.Desired || w.Available < w.Desired {
		return false
	}
	return w.Kind != "StatefulSet" || w.currentRev == "" || w.currentRev == w.updateRev
}

// InProgress: the controller is mid-rollout (as opposed to a workload that is
// fully updated but unhealthy, which a new commit didn't cause).
func (w Workload) InProgress() bool {
	return w.Observed < w.Generation || w.Updated < w.Desired ||
		(w.Kind == "StatefulSet" && w.currentRev != "" && w.currentRev != w.updateRev)
}

// ParseWorkloads parses `kubectl get deployments,statefulsets,daemonsets -o json`
// (a single object wrapped as {"items":[…]} works too).
func ParseWorkloads(data []byte) ([]Workload, error) {
	var list struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name              string            `json:"name"`
				Namespace         string            `json:"namespace"`
				Generation        int64             `json:"generation"`
				CreationTimestamp time.Time         `json:"creationTimestamp"`
				Labels            map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Replicas *int `json:"replicas"`
				Selector struct {
					MatchLabels map[string]string `json:"matchLabels"`
				} `json:"selector"`
				Template struct {
					Spec struct {
						Containers []struct {
							Name  string `json:"name"`
							Image string `json:"image"`
						} `json:"containers"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
			Status struct {
				ObservedGeneration int64 `json:"observedGeneration"`
				// Deployment / StatefulSet
				UpdatedReplicas   int    `json:"updatedReplicas"`
				ReadyReplicas     int    `json:"readyReplicas"`
				AvailableReplicas int    `json:"availableReplicas"`
				CurrentRevision   string `json:"currentRevision"`
				UpdateRevision    string `json:"updateRevision"`
				// DaemonSet
				DesiredNumberScheduled int `json:"desiredNumberScheduled"`
				UpdatedNumberScheduled int `json:"updatedNumberScheduled"`
				NumberAvailable        int `json:"numberAvailable"`
				NumberReady            int `json:"numberReady"`
				Conditions             []struct {
					Type    string `json:"type"`
					Status  string `json:"status"`
					Reason  string `json:"reason"`
					Message string `json:"message"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]Workload, 0, len(list.Items))
	for _, it := range list.Items {
		w := Workload{
			Kind: it.Kind, Namespace: it.Metadata.Namespace, Name: it.Metadata.Name,
			Created:    it.Metadata.CreationTimestamp,
			Generation: it.Metadata.Generation, Observed: it.Status.ObservedGeneration,
			KsName:      it.Metadata.Labels["kustomize.toolkit.fluxcd.io/name"],
			KsNamespace: it.Metadata.Labels["kustomize.toolkit.fluxcd.io/namespace"],
		}
		w.Selector = selectorString(it.Spec.Selector.MatchLabels)
		for _, c := range it.Spec.Template.Spec.Containers {
			w.Containers = append(w.Containers, Container{c.Name, c.Image})
		}
		desired := 1
		if it.Spec.Replicas != nil {
			desired = *it.Spec.Replicas
		}
		switch it.Kind {
		case "DaemonSet":
			w.Desired, w.Updated = it.Status.DesiredNumberScheduled, it.Status.UpdatedNumberScheduled
			w.Ready, w.Available = it.Status.NumberReady, it.Status.NumberAvailable
		case "StatefulSet":
			w.Desired, w.Updated = desired, it.Status.UpdatedReplicas
			w.Ready, w.Available = it.Status.ReadyReplicas, it.Status.ReadyReplicas
			w.currentRev, w.updateRev = it.Status.CurrentRevision, it.Status.UpdateRevision
		default:
			w.Desired, w.Updated = desired, it.Status.UpdatedReplicas
			w.Ready, w.Available = it.Status.ReadyReplicas, it.Status.AvailableReplicas
		}
		for _, c := range it.Status.Conditions {
			if c.Type == "Progressing" && c.Status == "False" && c.Reason == "ProgressDeadlineExceeded" {
				w.Failed, w.Message = true, c.Message
			}
		}
		out = append(out, w)
	}
	return out, nil
}

// Workloads lists the Flux-managed Deployments, StatefulSets and DaemonSets.
func (k Client) Workloads(ctxName string) ([]Workload, error) {
	out, err := k.Get(ctxName, "get", "deployments,statefulsets,daemonsets", "-A",
		"-l", "kustomize.toolkit.fluxcd.io/name", "-o", "json")
	if err != nil {
		return nil, err
	}
	return ParseWorkloads(out)
}

// ShortKind is "deploy", "sts", "ds" or "hr" for compact output.
func ShortKind(kind string) string {
	switch kind {
	case "Deployment":
		return "deploy"
	case "StatefulSet":
		return "sts"
	case "DaemonSet":
		return "ds"
	case "HelmRelease":
		return "hr"
	}
	return strings.ToLower(kind)
}

// selectorString is a label selector map as "k=v,k=v", sorted ("" when
// empty).
func selectorString(m map[string]string) string {
	sel := make([]string, 0, len(m))
	for k, v := range m {
		sel = append(sel, k+"="+v)
	}
	sort.Strings(sel)
	return strings.Join(sel, ",")
}
