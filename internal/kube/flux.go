package kube

import (
	"encoding/json"
	"sort"
	"strings"
)

const (
	KustomizationRes = "kustomizations.kustomize.toolkit.fluxcd.io"
	HelmReleaseRes   = "helmreleases.helm.toolkit.fluxcd.io"
	GitRepositoryRes = "gitrepositories.source.toolkit.fluxcd.io"
)

// FluxItem is a Kustomization, HelmRelease or GitRepository.
type FluxItem struct {
	Kind, Namespace, Name string
	Ready                 string // "True", "False" or "Unknown"
	Reason, Message       string
	Suspended             bool
	AppliedRevision       string // status.lastAppliedRevision
	AttemptedRevision     string // status.lastAttemptedRevision
	HandledReconcileAt    string // status.lastHandledReconcileAt
	SourceKind            string // spec.sourceRef (Kustomization only)
	SourceName            string
	SourceNamespace       string
	Path                  string // spec.path (Kustomization only)
	Generation, Observed  int64  // metadata.generation, status.observedGeneration
	KsName, KsNamespace   string // the Kustomization that applied this object
}

// AsWorkload lets a HelmRelease be tracked like a Deployment during a rollout.
func (it FluxItem) AsWorkload() Workload {
	w := Workload{Kind: it.Kind, Namespace: it.Namespace, Name: it.Name, Generation: it.Generation,
		Observed: it.Observed, Desired: 1, Updated: 1, KsName: it.KsName, KsNamespace: it.KsNamespace}
	if it.Ready == "True" {
		w.Ready = 1
	}
	if it.Ready == "False" && it.Observed >= it.Generation {
		w.Failed, w.Message = true, it.Message
	}
	return w
}

// Resource is the kubectl resource name, e.g. "kustomizations.kustomize.toolkit.fluxcd.io".
func (it FluxItem) Resource() string {
	switch it.Kind {
	case "HelmRelease":
		return HelmReleaseRes
	case "GitRepository":
		return GitRepositoryRes
	}
	return KustomizationRes
}

// Key identifies the item within a cluster.
func (it FluxItem) Key() string { return it.Kind + "/" + it.Namespace + "/" + it.Name }

// Failing: not suspended and Ready=False.
func (it FluxItem) Failing() bool { return !it.Suspended && it.Ready == "False" }

// FluxProblem is a Flux resource that is not ready or is suspended.
type FluxProblem struct {
	Kind, Namespace, Name, Message string
	Suspended                      bool
}

type FluxStatus struct {
	Installed                              bool
	Ready, Failing, Progressing, Suspended int
	Problems                               []FluxProblem
}

type fluxObject struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name       string            `json:"name"`
		Namespace  string            `json:"namespace"`
		Generation int64             `json:"generation"`
		Labels     map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Suspend bool   `json:"suspend"`
		URL     string `json:"url"`
		Path    string `json:"path"`
		Ref     struct {
			Branch string `json:"branch"`
		} `json:"ref"`
		SourceRef struct {
			Kind      string `json:"kind"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"sourceRef"`
	} `json:"spec"`
	Status struct {
		Conditions []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Reason  string `json:"reason"`
			Message string `json:"message"`
		} `json:"conditions"`
		ObservedGeneration     int64  `json:"observedGeneration"`
		LastAppliedRevision    string `json:"lastAppliedRevision"`
		LastAttemptedRevision  string `json:"lastAttemptedRevision"`
		LastHandledReconcileAt string `json:"lastHandledReconcileAt"`
		Artifact               struct {
			Revision string `json:"revision"`
		} `json:"artifact"`
	} `json:"status"`
}

func parseObjects(data []byte) ([]fluxObject, error) {
	var list struct {
		Items []fluxObject `json:"items"`
	}
	err := json.Unmarshal(data, &list)
	return list.Items, err
}

func (o fluxObject) ready() (status, reason, msg string) {
	status = "Unknown"
	for _, c := range o.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status, c.Reason, c.Message
		}
	}
	return
}

// ParseFluxItems parses a `kubectl get ... -o json` list of Kustomizations and
// HelmReleases.
func ParseFluxItems(data []byte) ([]FluxItem, error) {
	objs, err := parseObjects(data)
	if err != nil {
		return nil, err
	}
	items := make([]FluxItem, 0, len(objs))
	for _, o := range objs {
		it := FluxItem{
			Kind: o.Kind, Namespace: o.Metadata.Namespace, Name: o.Metadata.Name,
			Suspended:          o.Spec.Suspend,
			AppliedRevision:    o.Status.LastAppliedRevision,
			AttemptedRevision:  o.Status.LastAttemptedRevision,
			HandledReconcileAt: o.Status.LastHandledReconcileAt,
			SourceKind:         o.Spec.SourceRef.Kind,
			SourceName:         o.Spec.SourceRef.Name,
			SourceNamespace:    o.Spec.SourceRef.Namespace,
			Path:               o.Spec.Path,
			Generation:         o.Metadata.Generation,
			Observed:           o.Status.ObservedGeneration,
			KsName:             o.Metadata.Labels["kustomize.toolkit.fluxcd.io/name"],
			KsNamespace:        o.Metadata.Labels["kustomize.toolkit.fluxcd.io/namespace"],
		}
		if it.SourceNamespace == "" {
			it.SourceNamespace = it.Namespace
		}
		it.Ready, it.Reason, it.Message = o.ready()
		items = append(items, it)
	}
	SortFluxItems(items)
	return items, nil
}

// SortFluxItems orders failing first, then suspended, progressing, ready.
func SortFluxItems(items []FluxItem) {
	rank := func(it FluxItem) int {
		switch {
		case it.Failing():
			return 0
		case it.Suspended:
			return 1
		case it.Ready != "True":
			return 2
		}
		return 3
	}
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := rank(items[i]), rank(items[j])
		if ri != rj {
			return ri < rj
		}
		return items[i].Key() < items[j].Key()
	})
}

// Summarize counts items and lists the problems (failures first, then suspended).
func Summarize(items []FluxItem) FluxStatus {
	st := FluxStatus{Installed: true}
	for _, it := range items {
		p := FluxProblem{Kind: it.Kind, Namespace: it.Namespace, Name: it.Name, Message: it.Message}
		switch {
		case it.Suspended:
			st.Suspended++
			p.Suspended, p.Message = true, "suspended"
		case it.Ready == "True":
			st.Ready++
			continue
		case it.Ready == "False":
			st.Failing++
		default:
			st.Progressing++
			continue
		}
		st.Problems = append(st.Problems, p)
	}
	sort.SliceStable(st.Problems, func(i, j int) bool {
		return !st.Problems[i].Suspended && st.Problems[j].Suspended
	})
	return st
}

// ParseFlux parses and summarizes in one go.
func ParseFlux(data []byte) (FluxStatus, error) {
	items, err := ParseFluxItems(data)
	if err != nil {
		return FluxStatus{}, err
	}
	return Summarize(items), nil
}

// FluxItems lists the cluster's Kustomizations, HelmReleases and
// GitRepositories (a failing source leaves its Kustomizations "ready" on an old
// revision, so it must be checked too). installed is false when the Flux CRDs
// are missing.
func (k Client) FluxItems(ctxName string) (items []FluxItem, installed bool, err error) {
	out, err := k.run(ctxName, "get", KustomizationRes+","+HelmReleaseRes+","+GitRepositoryRes, "-A", "-o", "json")
	if err != nil {
		if strings.Contains(err.Error(), "doesn't have a resource type") {
			return nil, false, nil
		}
		return nil, false, err
	}
	items, err = ParseFluxItems(out)
	return items, err == nil, err
}

// Flux summarizes the cluster's Flux resources.
func (k Client) Flux(ctxName string) (FluxStatus, error) {
	items, installed, err := k.FluxItems(ctxName)
	if err != nil || !installed {
		return FluxStatus{}, err
	}
	return Summarize(items), nil
}

// GitRepo is a Flux GitRepository.
type GitRepo struct {
	Namespace, Name, URL string
	Branch               string // spec.ref.branch
	Revision             string // status.artifact.revision, e.g. "main@sha1:abc..."
	Ready, Message       string
}

func ParseGitRepos(data []byte) ([]GitRepo, error) {
	objs, err := parseObjects(data)
	if err != nil {
		return nil, err
	}
	out := make([]GitRepo, 0, len(objs))
	for _, o := range objs {
		r := GitRepo{Namespace: o.Metadata.Namespace, Name: o.Metadata.Name, URL: o.Spec.URL,
			Branch: o.Spec.Ref.Branch, Revision: o.Status.Artifact.Revision}
		r.Ready, _, r.Message = o.ready()
		out = append(out, r)
	}
	return out, nil
}

func (k Client) GitRepos(ctxName string) ([]GitRepo, error) {
	out, err := k.run(ctxName, "get", GitRepositoryRes, "-A", "-o", "json")
	if err != nil {
		return nil, err
	}
	return ParseGitRepos(out)
}

// NormalizeGitURL reduces a git URL to host/path so ssh and https forms of the
// same repository compare equal:
//
//	git@github.com:org/repo.git        → github.com/org/repo
//	ssh://git@github.com/org/repo      → github.com/org/repo
//	https://github.com/org/repo.git    → github.com/org/repo
func NormalizeGitURL(u string) string {
	u = strings.TrimSpace(strings.ToLower(u))
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	} else if at := strings.Index(u, "@"); at >= 0 {
		// scp-like: user@host:path
		u = strings.Replace(u[at+1:], ":", "/", 1)
	}
	if at := strings.Index(u, "@"); at >= 0 && at < strings.Index(u+"/", "/") {
		u = u[at+1:] // user@ in URL form
	}
	if slash := strings.Index(u, "/"); slash >= 0 {
		host := u[:slash]
		if c := strings.Index(host, ":"); c >= 0 { // drop port
			host = host[:c]
		}
		u = host + u[slash:]
	}
	u = strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
	return u
}

// AuthExpired reports whether a kubectl error means the login (OIDC token or
// credentials) is no longer valid, as opposed to the cluster being unreachable.
func AuthExpired(err error) bool {
	if err == nil {
		return false
	}
	m := strings.ToLower(err.Error())
	for _, s := range []string{"refresh token", "invalid_grant", "token is expired",
		"unauthorized", "you must be logged in", "provide credentials"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

// RevisionSHA extracts the commit sha from a Flux revision ("main@sha1:abc…",
// "sha1:abc…", "main/abc…" or a bare sha).
func RevisionSHA(rev string) string {
	if i := strings.LastIndex(rev, ":"); i >= 0 {
		return rev[i+1:]
	}
	if i := strings.LastIndex(rev, "/"); i >= 0 {
		return rev[i+1:]
	}
	return rev
}

// HasRevision reports whether a Flux revision ("main@sha1:abc…", "sha1:abc…"
// or a bare sha) points at commit sha.
func HasRevision(rev, sha string) bool {
	if rev == "" || sha == "" {
		return false
	}
	rev = RevisionSHA(rev)
	return strings.HasPrefix(rev, sha) || strings.HasPrefix(sha, rev) && len(rev) >= 7
}
