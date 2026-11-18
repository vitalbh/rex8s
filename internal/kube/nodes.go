package kube

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Node is a cluster node.
type Node struct {
	Name          string
	Ready         bool
	Pressure      []string // MemoryPressure, DiskPressure, PIDPressure that are True
	Roles         []string
	Version       string
	Created       time.Time
	Unschedulable bool
}

// ParseNodes parses `kubectl get nodes -o json`, in the order kubectl gave.
// Roles come from the node-role.kubernetes.io/* labels, sorted.
func ParseNodes(data []byte) ([]Node, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string            `json:"name"`
				CreationTimestamp time.Time         `json:"creationTimestamp"`
				Labels            map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Unschedulable bool `json:"unschedulable"`
			} `json:"spec"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
				NodeInfo struct {
					KubeletVersion string `json:"kubeletVersion"`
				} `json:"nodeInfo"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]Node, 0, len(list.Items))
	for _, it := range list.Items {
		n := Node{Name: it.Metadata.Name, Version: it.Status.NodeInfo.KubeletVersion,
			Created: it.Metadata.CreationTimestamp, Unschedulable: it.Spec.Unschedulable}
		for _, c := range it.Status.Conditions {
			switch {
			case c.Type == "Ready":
				n.Ready = c.Status == "True"
			case strings.HasSuffix(c.Type, "Pressure") && c.Status == "True":
				n.Pressure = append(n.Pressure, c.Type)
			}
		}
		for l := range it.Metadata.Labels {
			if r, ok := strings.CutPrefix(l, "node-role.kubernetes.io/"); ok && r != "" {
				n.Roles = append(n.Roles, r)
			}
		}
		sort.Strings(n.Roles)
		out = append(out, n)
	}
	return out, nil
}
