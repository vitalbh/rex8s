// Package guard decides whether a kubectl/helm/flux command mutates the cluster.
package guard

import "strings"

// Parsed holds the parts of a command line that matter.
type Parsed struct {
	Verb, Sub  string
	Context    string // --context / --kube-context
	Kubeconfig string // --kubeconfig
}

// global flags that consume the next argument
var valueFlags = map[string]bool{
	"-n": true, "--namespace": true, "--context": true, "--kube-context": true,
	"--kubeconfig": true, "-s": true, "--server": true, "--cluster": true, "--user": true,
}

func Parse(args []string) Parsed {
	var p Parsed
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "--") {
			p.setFlag(k, v)
			continue
		}
		if valueFlags[a] {
			if i+1 < len(args) {
				p.setFlag(a, args[i+1])
			}
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		switch {
		case p.Verb == "":
			p.Verb = a
		case p.Sub == "":
			p.Sub = a
		}
	}
	return p
}

func (p *Parsed) setFlag(k, v string) {
	switch k {
	case "--context", "--kube-context":
		p.Context = v
	case "--kubeconfig":
		p.Kubeconfig = v
	}
}

var risky = map[string]map[string]bool{
	"kubectl": set("apply", "create", "delete", "edit", "patch", "replace", "scale", "drain",
		"cordon", "uncordon", "taint", "label", "annotate", "set", "autoscale", "expose", "run", "rollout"),
	"helm": set("install", "upgrade", "uninstall", "delete", "rollback"),
	"flux": set("suspend", "resume", "delete", "create", "uninstall", "install", "bootstrap"),
}

// Risky reports whether the command changes cluster state.
func Risky(tool string, p Parsed) bool {
	if !risky[tool][p.Verb] {
		return false
	}
	if tool == "kubectl" && p.Verb == "rollout" && (p.Sub == "status" || p.Sub == "history") {
		return false
	}
	return true
}

// FluxReverts reports manual changes that Flux undoes on the next reconcile.
func FluxReverts(tool string, p Parsed) bool {
	if tool != "kubectl" {
		return false
	}
	switch p.Verb {
	case "apply", "create", "edit", "patch", "replace", "delete", "scale", "set", "label", "annotate":
		return true
	}
	return false
}

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}
