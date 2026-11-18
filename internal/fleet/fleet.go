// Package fleet reads from many clusters at once: which clusters a name
// means, the same query on each of them in parallel, and what to do with
// the ones that do not answer.
//
// It prints nothing: callers show progress and skipped clusters their own
// way (a stderr line, dim rows in a view), using ReadEach's callback for
// per-cluster progress.
package fleet

import (
	"cmp"
	"fmt"
	"sort"

	api "k8s.io/client-go/tools/clientcmd/api"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/kube"
)

// Unique drops aliases: contexts pointing at the same server (the first one
// wins, so pass them sorted as kube.Contexts does). A context without a
// server counts by its name.
func Unique(cs []kube.Context) []kube.Context {
	seen := map[string]bool{}
	var out []kube.Context
	for _, c := range cs {
		key := c.Server
		if key == "" {
			key = c.Name
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}
	return out
}

// All is every cluster: the visible contexts without aliases.
func All(raw *api.Config, cfg *config.Config) []kube.Context {
	return Unique(kube.Contexts(raw, cfg))
}

// Resolve finds the context the user means by name. The rule, everywhere:
//
//  1. a context with exactly that name, hidden ones included (naming a
//     context is deliberate);
//  2. otherwise the first visible context whose short name (from the rex8s
//     config) is name, in kube.Contexts order.
//
// An empty name is an error: callers that default to the current context
// check for it first.
func Resolve(raw *api.Config, cfg *config.Config, name string) (kube.Context, error) {
	if name != "" {
		if c, ok := kube.Find(raw, cfg, name); ok {
			return c, nil
		}
		for _, c := range kube.Contexts(raw, cfg) {
			if c.Short == name {
				return c, nil
			}
		}
	}
	return kube.Context{}, fmt.Errorf("no context or cluster named %q", name)
}

// Pick is the clusters a multi-cluster command works on: All when names is
// empty, otherwise each name resolved with Resolve, in the order given.
func Pick(raw *api.Config, cfg *config.Config, names []string) ([]kube.Context, error) {
	if len(names) == 0 {
		return All(raw, cfg), nil
	}
	out := make([]kube.Context, 0, len(names))
	for _, n := range names {
		c, err := Resolve(raw, cfg, n)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// Compare orders rows the same way in every multi-cluster list: by cluster
// short name, then namespace, then name. It returns -1, 0 or +1 like
// cmp.Compare.
func Compare(c1 kube.Context, ns1, name1 string, c2 kube.Context, ns2, name2 string) int {
	return cmp.Or(cmp.Compare(c1.Short, c2.Short), cmp.Compare(ns1, ns2), cmp.Compare(name1, name2))
}

// Sort sorts rows with Compare; ref gives a value's namespace and name.
func Sort[T any](rows []kube.On[T], ref func(T) (ns, name string)) {
	sort.SliceStable(rows, func(i, j int) bool {
		ns1, n1 := ref(rows[i].V)
		ns2, n2 := ref(rows[j].V)
		return Compare(rows[i].C, ns1, n1, rows[j].C, ns2, n2) < 0
	})
}
