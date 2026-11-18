package guard

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/kube"
	"github.com/vitalbh/rex8s/internal/pathx"
)

// errExit: the reason was already printed; exit 1.
var errExit = errors.New("exit 1")

// Run executes tool (kubectl/helm/flux) under the production policy (see
// policy.go): a command that changes a production cluster, or one the guard
// can't classify, asks first; without a terminal it is refused; REX8S_FORCE=1
// lets it through. With guard: false in the config it runs the tool and
// nothing else: no question, no audit line.
//
// It lives outside the TUI binary on purpose: Bubble Tea's init() queries the
// terminal background color and can block for up to 5s on terminals that
// don't answer — unacceptable in a wrapper that runs on every kubectl.
func Run(tool string, args []string, skipDir string, cfg *config.Config) error {
	bin := pathx.Real(tool, skipDir)
	if bin == "" {
		return fmt.Errorf("%s not found in PATH", tool)
	}
	if !cfg.GuardOn() {
		return sys.exec(bin, append([]string{tool}, args...))
	}
	err := run(tool, bin, args, cfg)
	switch {
	case errors.Is(err, ErrRefused):
		fmt.Fprintln(sys.stderr, "rex8s guard: "+err.Error())
		os.Exit(1)
	case errors.Is(err, ErrCancelled), errors.Is(err, errExit):
		os.Exit(1)
	}
	return err
}

func run(tool, bin string, args []string, cfg *config.Config) error {
	p := Parse(tool, args)
	c, cerr := Target(cfg, tool, p)

	// a pane showing every namespace: reads get -A, the rest needs -n
	if cerr == nil && c.AllNS {
		adapted, msg := AllNamespacesArgs(tool, p, args)
		if msg != "" {
			fmt.Fprintln(sys.stderr, "rex8s: "+msg)
			return errExit
		}
		args = adapted
	}
	exec := func() error { return sys.exec(bin, append([]string{tool}, args...)) }

	risk := Classify(tool, p)
	// not production, or a kubeconfig/context kubectl itself will fail on
	if risk == Safe || cerr != nil || !c.Prod {
		return exec()
	}
	var notes []string
	if risk == Unknown {
		notes = append(notes, "rex8s doesn't know this command (or a flag before it), so it asks as for a change.")
	}
	if FluxReverts(tool, p) {
		notes = append(notes, "reminder: with Flux, manual changes to managed resources are reverted on the next reconcile.")
	}
	if err := decide([]string{c.Short}, tool+" "+strings.Join(args, " "), risk == Unknown, notes...); err != nil {
		return err
	}
	return exec()
}

// Target resolves the cluster a command acts on, and whether it is
// production. The context comes from --context/--kube-context (helm:
// HELM_KUBECONTEXT), else the kubeconfig's current one; then --cluster and
// --server (helm: --kube-apiserver, HELM_KUBEAPISERVER) can point it at
// another server, which decides production. --user naming the credentials of
// a production context counts as production too.
func Target(cfg *config.Config, tool string, p Parsed) (kube.Context, error) {
	name, server := p.Context, p.Server
	if tool == "helm" {
		if name == "" {
			name = sys.getenv("HELM_KUBECONTEXT")
		}
		if server == "" {
			server = sys.getenv("HELM_KUBEAPISERVER")
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if p.Kubeconfig != "" {
		rules.ExplicitPath = p.Kubeconfig
	}
	raw, err := rules.Load()
	if err != nil {
		return kube.Context{}, err
	}
	if name == "" {
		name = raw.CurrentContext
	}
	c, ok := kube.Find(raw, cfg, name)
	if !ok {
		if p.Cluster == "" && server == "" {
			return kube.Context{}, fmt.Errorf("context %q not found", name)
		}
		c = kube.Context{Name: name}
	}

	eff := c.Server
	if cl := raw.Clusters[p.Cluster]; p.Cluster != "" && cl != nil {
		eff = cl.Server
	}
	if server != "" {
		eff = server
	}
	if eff != c.Server || !ok {
		label := name
		if label == "" {
			label = p.Cluster
		}
		c.Server = eff
		c.Short, c.Prod = cfg.Lookup(eff, label)
	}

	if p.User != "" && !c.Prod {
		var names []string
		for n, kc := range raw.Contexts {
			if kc.AuthInfo == p.User {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			if d, _ := kube.Find(raw, cfg, n); d.Prod {
				c.Prod, c.Short = true, d.Short
				break
			}
		}
	}
	return c, nil
}
