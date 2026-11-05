// Package config reads ~/.config/rex/rex8s/config.yaml: which clusters are
// production, each cluster's short name, and which contexts to hide from the
// picker.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// Cluster identifies a cluster by its server URL (substring), not by context
// name, so every alias of the same cluster gets the same marking.
type Cluster struct {
	Match string `json:"match"`
	Name  string `json:"name"`
	Prod  bool   `json:"prod"`
	Login string `json:"login"` // login page for kauth (overrides login_url)
}

// Forward is a favorite port-forward.
type Forward struct {
	Name      string `json:"name"`
	Cluster   string `json:"cluster"` // short name or context
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	Port      int    `json:"port"`  // the service port
	Local     int    `json:"local"` // preferred local port (default: same as port)
	Path      string `json:"path"`  // opened in the browser, e.g. /dashboards
}

type Config struct {
	Kubeconfig string    `json:"kubeconfig"`
	Hide       []string  `json:"hide"`
	Clusters   []Cluster `json:"clusters"`
	// ProdBackground tints the background of production panes. Hex color;
	// "off" disables it. Default: a dark red.
	ProdBackground string `json:"prod_background"`
	// MonitorInterval is how often the Flux monitor polls, e.g. "2m".
	MonitorInterval string `json:"monitor_interval"`
	// LoginURL is the page kauth opens to log in again (e.g. a Dex
	// authenticator that prints kubectl config commands).
	LoginURL string `json:"login_url"`
	// AICommand runs kwhy's explanation: "claude" by default (claude-auto
	// when present); "off" disables kwhy.
	AICommand string `json:"ai_command"`
	// AskAgent is the agent kask talks through: claude (default) or codex.
	AskAgent string `json:"ask_agent"`
	// AILanguage is the language kwhy answers in, e.g. "pt-BR". Default: English.
	AILanguage string `json:"ai_language"`
	// Notifications: macOS notifications from fwatch and the Flux monitor.
	// Default true; set false to turn them off.
	Notifications *bool `json:"notifications"`
	// NodeDebugImage is the image knode runs on the node. Default busybox.
	NodeDebugImage string `json:"node_debug_image"`
	// GitopsRepos are local clones of the Flux GitOps repositories, for kgit
	// (matched by remote URL). The current directory's repository is tried too.
	GitopsRepos []string `json:"gitops_repos"`
	// Groups name sets of clusters for kmulti, e.g. prod: [dal, sp, mx].
	Groups map[string][]string `json:"groups"`
	// Forwards are favorite port-forwards: kpf NAME (and the Raycast menu
	// bar) starts one and opens the browser.
	Forwards []Forward `json:"forwards"`
	// NetDebugImage is the image knet runs in the namespace. Default netshoot.
	NetDebugImage string `json:"net_debug_image"`

	path string
}

// Path returns the config file path: $REX8S_CONFIG or
// $XDG_CONFIG_HOME/rex/rex8s/config.yaml (default ~/.config/rex/rex8s/config.yaml).
func Path() string {
	if p := os.Getenv("REX8S_CONFIG"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(home(), ".config")
	}
	return filepath.Join(base, "rex", "rex8s", "config.yaml")
}

// Load reads the file. A missing file yields an empty config (nothing is production).
func Load() (*Config, error) {
	p := Path()
	c := &Config{path: p}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	c.path = p
	return c, nil
}

func (c *Config) File() string { return c.path }

// GitopsDirs returns gitops_repos with ~ expanded.
func (c *Config) GitopsDirs() []string {
	out := make([]string, 0, len(c.GitopsRepos))
	for _, d := range c.GitopsRepos {
		out = append(out, expand(d))
	}
	return out
}

// KubeconfigPath is the "master" kubeconfig that panes are derived from.
func (c *Config) KubeconfigPath() string {
	if c.Kubeconfig != "" {
		return expand(c.Kubeconfig)
	}
	if kc := os.Getenv("KUBECONFIG"); kc != "" && !strings.Contains(kc, string(os.PathListSeparator)) {
		return kc
	}
	return filepath.Join(home(), ".kube", "config")
}

// NotificationsOn reports whether macOS notifications are enabled.
func (c *Config) NotificationsOn() bool { return c.Notifications == nil || *c.Notifications }

// Login returns the login page for the cluster at server, or "".
func (c *Config) Login(server string) string {
	for _, cl := range c.Clusters {
		if cl.Match != "" && strings.Contains(server, cl.Match) && cl.Login != "" {
			return cl.Login
		}
	}
	return c.LoginURL
}

// Tint is the background color for production panes, or "" when disabled.
func (c *Config) Tint() string {
	switch strings.ToLower(strings.TrimSpace(c.ProdBackground)) {
	case "":
		return "#2b1216"
	case "off", "none", "false":
		return ""
	}
	return c.ProdBackground
}

// Hidden reports whether the context is hidden from the picker (exact match,
// including whitespace).
func (c *Config) Hidden(ctx string) bool {
	for _, h := range c.Hide {
		if h == ctx {
			return true
		}
	}
	return false
}

// Lookup returns the short name and whether the cluster is production, based
// on the server URL. Without a matching rule it uses the context name (spaces
// become _) and is not production.
func (c *Config) Lookup(server, ctx string) (short string, prod bool) {
	for _, cl := range c.Clusters {
		if cl.Match != "" && strings.Contains(server, cl.Match) {
			name := cl.Name
			if name == "" {
				name = ctx
			}
			return name, cl.Prod
		}
	}
	return strings.ReplaceAll(strings.TrimSpace(ctx), " ", "_"), false
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func expand(p string) string {
	if p == "~" {
		return home()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home(), p[2:])
	}
	return p
}
