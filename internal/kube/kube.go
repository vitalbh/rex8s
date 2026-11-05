// Package kube handles kubeconfig (via clientcmd, no subprocess) and queries
// the cluster through kubectl, so any auth plugin keeps working.
package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

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
	Namespace string
	Short     string
	Prod      bool
	AllNS     bool // the pane shows every namespace (namespace "*")
}

func Load(path string) (*api.Config, error) { return clientcmd.LoadFromFile(path) }

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
		ctx.Server = cl.Server
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
	raw, err := clientcmd.LoadFromFile(src)
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

// ── queries via kubectl ─────────────────────────────────────────────────

type Client struct {
	Kubectl    string
	Kubeconfig string
	Timeout    time.Duration
}

func (k Client) run(ctxName string, args ...string) ([]byte, error) {
	t := k.Timeout
	if t == 0 {
		t = 10 * time.Second
	}
	c, cancel := context.WithTimeout(context.Background(), t)
	defer cancel()
	full := []string{"--kubeconfig", k.Kubeconfig}
	if ctxName != "" {
		full = append(full, "--context", ctxName)
	}
	full = append(full, args...)
	full = append(full, fmt.Sprintf("--request-timeout=%ds", int(t.Seconds())-1))
	cmd := exec.CommandContext(c, k.Kubectl, full...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := lastLine(stderr.String())
		if errors.Is(c.Err(), context.DeadlineExceeded) {
			msg = "timeout"
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	return out, nil
}

// Get runs a read-only kubectl command against ctxName and returns stdout.
func (k Client) Get(ctxName string, args ...string) ([]byte, error) { return k.run(ctxName, args...) }

var nsCache sync.Map // kubeconfig|ctx → []string

// Namespaces lists namespaces (cached in memory per process).
func (k Client) Namespaces(ctxName string) ([]string, error) {
	key := k.Kubeconfig + "|" + ctxName
	if v, ok := nsCache.Load(key); ok {
		return v.([]string), nil
	}
	out, err := k.run(ctxName, "get", "ns", "-o", `jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}`)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			names = append(names, l)
		}
	}
	nsCache.Store(key, names)
	return names, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
