// Package kube handles kubeconfig (via clientcmd, no subprocess) and queries
// the cluster through kubectl, so any auth plugin keeps working.
package kube

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	api "k8s.io/client-go/tools/clientcmd/api"

	"github.com/vitalbh/rex8s/internal/config"
)

// Context is a kubeconfig context enriched with the rex8s config.
type Context struct {
	Name      string
	Cluster   string
	User      string
	Server    string
	ProxyURL  string // the cluster's proxy-url, if any
	Namespace string
	Short     string
	Prod      bool
	AllNS     bool // the pane shows every namespace (namespace "*")
}

// Load reads a kubeconfig. path may be a list (a:b, like KUBECONFIG): the files
// are merged with kubectl's rules (first file wins for current-context and for
// entries with the same name).
func Load(path string) (*api.Config, error) {
	if !strings.Contains(path, string(os.PathListSeparator)) {
		return clientcmd.LoadFromFile(path)
	}
	rules := &clientcmd.ClientConfigLoadingRules{Precedence: filepath.SplitList(path)}
	return rules.Load()
}

// Contexts lists the visible contexts, sorted by short name, then name.
func Contexts(raw *api.Config, cfg *config.Config) []Context {
	var out []Context
	for name, c := range raw.Contexts {
		if cfg.Hidden(name) {
			continue
		}
		out = append(out, describe(raw, cfg, name, c))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Short != out[j].Short {
			return out[i].Short < out[j].Short
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Find returns a context by exact name (hidden ones included).
func Find(raw *api.Config, cfg *config.Config, name string) (Context, bool) {
	c, ok := raw.Contexts[name]
	if !ok {
		return Context{}, false
	}
	return describe(raw, cfg, name, c), true
}

// AllNamespaces is the namespace a pane uses for "every namespace": kubectl
// (through the guard) gets -A, the k* commands list every namespace.
const AllNamespaces = "*"

func describe(raw *api.Config, cfg *config.Config, name string, c *api.Context) Context {
	ctx := Context{Name: name, Cluster: c.Cluster, User: c.AuthInfo, Namespace: c.Namespace}
	if ctx.Namespace == AllNamespaces {
		ctx.Namespace, ctx.AllNS = "", true
	}
	if cl := raw.Clusters[c.Cluster]; cl != nil {
		ctx.Server, ctx.ProxyURL = cl.Server, cl.ProxyURL
	}
	ctx.Short, ctx.Prod = cfg.Lookup(ctx.Server, name)
	return ctx
}

// WritePane writes a pane's kubeconfig to dst: only current-context and the
// context (with an optional namespace), WITHOUT clusters or credentials. The
// pane uses KUBECONFIG=dst:src (see PaneKubeconfig), so clusters and users
// come from the master kubeconfig.
//
// Why not copy the credentials: with OIDC (rotating refresh tokens), kubectl
// writes the refreshed token back to the file the user entry came from. In a
// copy, the new token would live only there and the master's refresh token
// would be invalidated ("already claimed by another client"). By referencing
// the master, all panes share the same credential.
func WritePane(src, ctxName, ns, dst string) error {
	raw, err := Load(src)
	if err != nil {
		return err
	}
	c, ok := raw.Contexts[ctxName]
	if !ok {
		return fmt.Errorf("context %q not found in %s", ctxName, src)
	}
	cc := c.DeepCopy()
	cc.LocationOfOrigin = ""
	if ns != "" {
		cc.Namespace = ns
	}
	pane := api.NewConfig()
	pane.CurrentContext = ctxName
	pane.Contexts[ctxName] = cc
	return write(pane, dst)
}

// PaneKubeconfig is a pane's KUBECONFIG value: the pane file first (it wins
// for current-context and the context), the master second.
func PaneKubeconfig(pane, src string) string {
	return pane + string(os.PathListSeparator) + src
}

// SetNamespace changes the namespace of the file's current context.
func SetNamespace(path, ns string) error {
	raw, err := clientcmd.LoadFromFile(path)
	if err != nil {
		return err
	}
	c, ok := raw.Contexts[raw.CurrentContext]
	if !ok {
		return fmt.Errorf("%s: no current-context", path)
	}
	c.Namespace = ns
	return write(raw, path)
}

func write(raw *api.Config, dst string) error {
	data, err := clientcmd.Write(*raw)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".kubeconfig-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// Current resolves the context in use: ctxOverride (--context) or the
// kubeconfig's current-context (kubeconfigOverride, or KUBECONFIG/~/.kube/config).
func Current(cfg *config.Config, ctxOverride, kubeconfigOverride string) (Context, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigOverride != "" {
		rules.ExplicitPath = kubeconfigOverride
	}
	raw, err := rules.Load()
	if err != nil {
		return Context{}, err
	}
	name := raw.CurrentContext
	if ctxOverride != "" {
		name = ctxOverride
	}
	c, ok := raw.Contexts[name]
	if !ok {
		return Context{}, fmt.Errorf("context %q not found", name)
	}
	return describe(raw, cfg, name, c), nil
}

// On is a value read from a cluster (a pod, a service…) with the context it
// came from. Multi-cluster lists are []On[T].
type On[T any] struct {
	C Context
	V T
}
