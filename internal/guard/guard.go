// Package guard decides whether a kubectl/helm/flux command changes the
// cluster, and holds rex8s's one production policy: Run for the wrappers,
// ConfirmChange for rex8s commands that change a cluster themselves.
package guard

import "strings"

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

// cluster-scoped kinds (any namespace works for them)
var clusterScoped = set("node", "nodes", "no", "namespace", "namespaces", "ns", "pv", "persistentvolume",
	"persistentvolumes", "storageclass", "storageclasses", "sc", "clusterrole", "clusterroles",
	"clusterrolebinding", "clusterrolebindings", "crd", "crds", "customresourcedefinition",
	"customresourcedefinitions", "apiservice", "apiservices", "priorityclass", "priorityclasses", "pc",
	"ingressclass", "ingressclasses", "csidriver", "csidrivers", "csinode", "csinodes", "runtimeclass",
	"runtimeclasses", "mutatingwebhookconfiguration", "mutatingwebhookconfigurations",
	"validatingwebhookconfiguration", "validatingwebhookconfigurations", "volumeattachment", "volumeattachments")

func isClusterScoped(res string) bool {
	if res == "" {
		return false
	}
	for _, r := range strings.Split(res, ",") {
		r = strings.ToLower(strings.SplitN(r, "/", 2)[0])
		r = strings.SplitN(r, ".", 2)[0] // nodes.metrics.k8s.io → nodes
		if !clusterScoped[r] {
			return false
		}
	}
	return true
}

// commands that never talk to a namespace
var noNamespace = map[string]map[string]bool{
	"kubectl": set("version", "config", "api-resources", "api-versions", "cluster-info", "explain",
		"completion", "plugin", "kustomize", "certificate", "cordon", "uncordon", "drain", "taint", "help", "alpha", "krew", "ctx", "ns"),
	"helm": set("repo", "search", "version", "template", "dependency", "dep", "plugin", "env", "show",
		"inspect", "pull", "fetch", "package", "lint", "create", "completion", "help", "registry", "push", "verify"),
}

// AllNamespacesArgs adapts a command run in an "every namespace" pane:
// reads that can span namespaces get -A; one that needs a namespace and
// has none gets a message instead of a confusing "not found in *".
// Returns the args to run, or msg when the command can't run as is.
func AllNamespacesArgs(tool string, p Parsed, args []string) (out []string, msg string) {
	if p.Namespace != "" || p.AllNS || p.Verb == "" || noNamespace[tool][p.Verb] {
		return args, ""
	}
	withA := func() []string { return append(append([]string{}, args...), "-A") }
	need := "this pane shows every namespace: add -n NAMESPACE (or kns NAMESPACE)"
	switch tool {
	case "kubectl":
		switch p.Verb {
		case "get", "describe":
			if len(p.Rest) == 0 || isClusterScoped(p.Rest[0]) {
				return args, ""
			}
			named := len(p.Rest) > 1 || strings.Contains(p.Rest[0], "/")
			if named {
				return nil, need
			}
			return withA(), ""
		case "top":
			if p.Sub == "pod" || p.Sub == "pods" || p.Sub == "po" {
				if len(p.Rest) > 1 {
					return nil, need
				}
				return withA(), ""
			}
			return args, ""
		case "events":
			return withA(), ""
		}
		if len(p.Rest) > 0 && isClusterScoped(p.Rest[0]) {
			return args, ""
		}
		return nil, need
	case "helm":
		if p.Verb == "list" || p.Verb == "ls" {
			return withA(), ""
		}
		return nil, need
	case "flux":
		if p.Verb == "get" {
			return withA(), ""
		}
	}
	return args, ""
}
