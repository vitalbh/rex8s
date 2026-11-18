package kube

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ServicePort is one TCP port of a Service.
type ServicePort struct {
	Name        string
	Port        int
	Protocol    string
	AppProtocol string
}

// Service is a Service with its TCP ports (port-forward is TCP only).
type Service struct {
	Namespace string
	Name      string
	Type      string
	Ports     []ServicePort
}

// ParseServices parses `kubectl get services -o json`, keeping only TCP
// ports and dropping services with none.
func ParseServices(data []byte) ([]Service, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				Type  string `json:"type"`
				Ports []struct {
					Name        string `json:"name"`
					Port        int    `json:"port"`
					Protocol    string `json:"protocol"`
					AppProtocol string `json:"appProtocol"`
				} `json:"ports"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]Service, 0, len(list.Items))
	for _, it := range list.Items {
		s := Service{Namespace: it.Metadata.Namespace, Name: it.Metadata.Name, Type: it.Spec.Type}
		for _, p := range it.Spec.Ports {
			if p.Protocol == "" || p.Protocol == "TCP" { // port-forward is TCP only
				s.Ports = append(s.Ports, ServicePort{Name: p.Name, Port: p.Port, Protocol: "TCP", AppProtocol: p.AppProtocol})
			}
		}
		if len(s.Ports) > 0 {
			out = append(out, s)
		}
	}
	return out, nil
}

// PortsText is "80/http 443": each port, with its name when it has one.
func (s Service) PortsText() string {
	parts := make([]string, len(s.Ports))
	for i, p := range s.Ports {
		parts[i] = strconv.Itoa(p.Port)
		if p.Name != "" {
			parts[i] += "/" + p.Name
		}
	}
	return strings.Join(parts, " ")
}
