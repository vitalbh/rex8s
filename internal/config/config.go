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
	"reflect"
	"sort"
	"strings"
	"time"

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
	// ClusterBrowser is the left pane of the ⇧⌘K layout: "kview" (default)
	// or "k9s".
	ClusterBrowser string `json:"cluster_browser"`
	// ForwardExpire: a background port-forward with no connection for this
	// long goes away by itself, e.g. "8h" (the default); "off" keeps them
	// until stopped.
	ForwardExpire string `json:"forward_expire"`
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
	// LogLinks: which cluster URLs in logs become clickable: "namespace"
	// (default: services of the stream's namespace and its pods), "all"
	// (services of any namespace too) or "off".
	LogLinks string `json:"log_links"`

	// Warnings: what Load found wrong but could live with (unknown keys,
	// invalid values that fall back to the default). For the user to see.
	Warnings []string `json:"-"`

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
	c.Warnings = c.check(data)
	return c, nil
}

// check returns what is wrong in the file without making it unusable: keys
// rex8s doesn't know (a typo silently does nothing) and values it ignores.
func (c *Config) check(data []byte) []string {
	var w []string
	var raw map[string]any
	if yaml.Unmarshal(data, &raw) == nil {
		w = append(w, unknownKeys("", raw, reflect.TypeOf(Config{}))...)
		for _, l := range []struct {
			key string
			t   reflect.Type
		}{{"clusters", reflect.TypeOf(Cluster{})}, {"forwards", reflect.TypeOf(Forward{})}} {
			items, _ := raw[l.key].([]any)
			for i, it := range items {
				if m, ok := it.(map[string]any); ok {
					w = append(w, unknownKeys(fmt.Sprintf("%s[%d].", l.key, i), m, l.t)...)
				}
			}
		}
	}
	if err := ValidForwardExpire(c.ForwardExpire); err != nil {
		w = append(w, err.Error()+"; using 8h")
	}
	if v := c.MonitorInterval; v != "" {
		if d, err := time.ParseDuration(v); err != nil || d <= 0 {
			w = append(w, fmt.Sprintf("monitor_interval: %q is not a duration like 2m", v))
		}
	}
	oneOf := func(key, v string, ok ...string) {
		if v == "" {
			return
		}
		for _, o := range ok {
			if strings.EqualFold(strings.TrimSpace(v), o) {
				return
			}
		}
		w = append(w, fmt.Sprintf("%s: %q is not one of %s", key, v, strings.Join(ok, ", ")))
	}
	oneOf("cluster_browser", c.ClusterBrowser, "kview", "k9s")
	oneOf("ask_agent", c.AskAgent, "claude", "codex")
	oneOf("log_links", c.LogLinks, "namespace", "all", "off", "false")
	for i, f := range c.Forwards {
		if f.Name == "" || f.Service == "" || f.Port <= 0 {
			w = append(w, fmt.Sprintf("forwards[%d]: name, service and port are required", i))
		}
	}
	return w
}

// unknownKeys: the keys of m that no json tag of struct type t names.
func unknownKeys(prefix string, m map[string]any, t reflect.Type) []string {
	known := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			known[name] = true
		}
	}
	var out []string
	for k := range m {
		if !known[k] {
			out = append(out, fmt.Sprintf("unknown setting %s%s (ignored)", prefix, k))
		}
	}
	sort.Strings(out)
	return out
}

// ValidForwardExpire reports whether v is a forward_expire value rex8s
// understands: empty, a positive duration (8h, 90m), or off.
func ValidForwardExpire(v string) error {
	if _, ok := expiry(v); !ok {
		return fmt.Errorf("forward_expire: %q is not a duration like 8h, or off", v)
	}
	return nil
}

// expiry parses forward_expire; ok is false for values it doesn't know.
// YAML reads an unquoted off/no as a boolean, which arrives here as "false".
func expiry(v string) (d time.Duration, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return 8 * time.Hour, true
	case "off", "0", "never", "false", "no", "none":
		return 0, true
	}
	if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil && d > 0 {
		return d, true
	}
	return 8 * time.Hour, false
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

// KubeconfigPath is the "master" kubeconfig that panes are derived from. The
// setting may list several files like KUBECONFIG (a:b): contexts from all of them
// are offered, and kubectl writes each credential back to the file it came from.
func (c *Config) KubeconfigPath() string {
	if c.Kubeconfig != "" {
		parts := filepath.SplitList(c.Kubeconfig)
		for i, p := range parts {
			parts[i] = expand(strings.TrimSpace(p))
		}
		return strings.Join(parts, string(os.PathListSeparator))
	}
	if kc := os.Getenv("KUBECONFIG"); kc != "" && !strings.Contains(kc, string(os.PathListSeparator)) {
		return kc
	}
	return filepath.Join(home(), ".kube", "config")
}

// UseK9s: the ⇧⌘K layout opens k9s instead of kview.
func (c *Config) UseK9s() bool { return strings.EqualFold(strings.TrimSpace(c.ClusterBrowser), "k9s") }

// ForwardExpiry is ForwardExpire as a duration: 8h by default (and for
// values it doesn't understand), 0 for "off".
func (c *Config) ForwardExpiry() time.Duration {
	d, _ := expiry(c.ForwardExpire)
	return d
}

// LogLinksMode is log_links: "namespace" (default), "all" or "off".
func (c *Config) LogLinksMode() string {
	switch strings.ToLower(strings.TrimSpace(c.LogLinks)) {
	case "all":
		return "all"
	case "off", "false":
		return "off"
	}
	return "namespace"
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
