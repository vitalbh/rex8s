package guard

import "strings"

// Parsed holds the parts of a command line that matter.
type Parsed struct {
	Verb, Sub  string
	Rest       []string // positional arguments after the verb
	Context    string   // --context / --kube-context
	Kubeconfig string   // --kubeconfig
	Cluster    string   // --cluster (kubectl, flux)
	Server     string   // -s/--server (kubectl, flux), --kube-apiserver (helm)
	User       string   // --user (kubectl, flux)
	Namespace  string   // -n / --namespace
	AllNS      bool     // -A / --all-namespaces
	Help       bool     // -h / --help: prints usage, changes nothing
	DryRun     bool     // --dry-run[=client|server]: changes nothing
	Export     bool     // flux create … --export: prints YAML, changes nothing

	// A flag the guard doesn't know came before the verb: it may or may not
	// take a value, so the verb is uncertain. Alt is the parse where the flag
	// takes the next argument (set when there was exactly one such flag).
	Alt           *Parsed
	unknownBefore int  // unknown flags before the verb
	dashesFirst   bool // arguments after a "--" that came before any verb
}

type flagTable struct{ value, boolean map[string]bool }

// The global flags of each tool (`kubectl options`, `helm --help`,
// `flux --help`). Before the verb, a flag in neither list makes the verb
// uncertain (see Parsed.Alt): cobra takes the next argument as its value, so
// `kubectl -v 6 delete pod x` must not read "6" as the command.
var globalFlags = map[string]flagTable{
	"kubectl": {
		value: set("--as", "--as-group", "--as-uid", "--cache-dir", "--certificate-authority",
			"--client-certificate", "--client-key", "--cluster", "--context", "--kubeconfig",
			"--log-flush-frequency", "-n", "--namespace", "--password", "--profile", "--profile-output",
			"--request-timeout", "-s", "--server", "--tls-server-name", "--token", "--user", "--username",
			"-v", "--v", "--vmodule", "--log-backtrace-at", "--log-dir", "--log-file",
			"--log-file-max-size", "--stderrthreshold", "--kuberc"),
		boolean: set("--insecure-skip-tls-verify", "--match-server-version", "--warnings-as-errors",
			"--disable-compression", "--alsologtostderr", "--logtostderr", "--skip-headers",
			"--skip-log-headers", "--one-output", "--add-dir-header", "-h", "--help", "-A", "--all-namespaces"),
	},
	"helm": {
		value: set("--burst-limit", "--kube-apiserver", "--kube-as-group", "--kube-as-user", "--kube-ca-file",
			"--kube-context", "--kube-tls-server-name", "--kube-token", "--kubeconfig", "-n", "--namespace",
			"--qps", "--registry-config", "--repository-cache", "--repository-config", "--content-cache"),
		boolean: set("--debug", "--kube-insecure-skip-tls-verify", "-h", "--help", "-A", "--all-namespaces"),
	},
	"flux": {
		value: set("--as", "--as-group", "--as-uid", "--cache-dir", "--certificate-authority",
			"--client-certificate", "--client-key", "--cluster", "--context", "--kube-api-burst",
			"--kube-api-qps", "--kubeconfig", "-n", "--namespace", "-s", "--server", "--timeout",
			"--tls-server-name", "--token", "--user"),
		boolean: set("--disable-compression", "--insecure-skip-tls-verify", "--verbose", "-h", "--help",
			"-A", "--all-namespaces"),
	},
}

// flags of common subcommands that take a value, so their values aren't
// taken for resource names
var localValue = set("-o", "--output", "-l", "--selector", "--field-selector", "-L", "--label-columns",
	"--sort-by", "--template", "-c", "--container", "--tail", "--since", "--since-time", "--chunk-size",
	"--max-log-requests", "-f", "--filename", "-k", "--kustomize", "-p", "--patch", "--type", "--image",
	"--replicas", "--set", "--set-string", "--set-file", "--set-json", "--values", "--version",
	"--description", "--post-renderer", "--revision", "--for")

// Parse reads a kubectl/helm/flux command line (the arguments after the tool).
func Parse(tool string, args []string) Parsed {
	p := parse(tool, args, false)
	if p.unknownBefore == 1 {
		alt := parse(tool, args, true)
		p.Alt = &alt
	}
	return p
}

// parse reads args; unknownTakesValue decides whether a flag the guard doesn't
// know, before the verb, consumes the next argument.
func parse(tool string, args []string, unknownTakesValue bool) Parsed {
	g, ok := globalFlags[tool]
	if !ok {
		g = globalFlags["kubectl"]
	}
	takesValue := func(f string) bool { return g.value[f] || localValue[f] }
	var p Parsed
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			p.dashesFirst = p.Verb == "" && i+1 < len(args)
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			switch {
			case p.Verb == "":
				p.Verb = a
			default:
				if p.Sub == "" {
					p.Sub = a
				}
				p.Rest = append(p.Rest, a)
			}
			continue
		}
		name, val, inline := a, "", false
		switch {
		case strings.HasPrefix(a, "--"):
			name, val, inline = strings.Cut(a, "=")
		case len(a) > 2: // -v6, -n=api, -nfoo; or short booleans together (-it)
			if !takesValue(a[:2]) {
				continue // cobra never takes a value for these
			}
			name, val, inline = a[:2], strings.TrimPrefix(a[2:], "="), true
		}
		switch {
		case inline:
			p.setFlag(tool, name, val)
		case takesValue(name):
			if i+1 < len(args) {
				p.setFlag(tool, name, args[i+1])
			}
			i++
		case p.Verb == "" && !g.boolean[name]:
			p.unknownBefore++
			if unknownTakesValue {
				i++
			}
		default:
			p.setFlag(tool, name, "true")
		}
	}
	return p
}

func (p *Parsed) setFlag(tool, k, v string) {
	on := v != "false" && v != "0"
	switch k {
	case "--context", "--kube-context":
		p.Context = v
	case "--kubeconfig":
		p.Kubeconfig = v
	case "--cluster":
		p.Cluster = v
	case "-s", "--server":
		if tool != "helm" {
			p.Server = v
		}
	case "--kube-apiserver":
		p.Server = v
	case "--user":
		p.User = v
	case "-n", "--namespace":
		p.Namespace = v
	case "-A", "--all-namespaces":
		p.AllNS = on
	case "-h", "--help":
		p.Help = on
	case "--dry-run":
		p.DryRun = on && v != "none"
	case "--export":
		p.Export = on
	}
}

// Risk is what a command does to the cluster.
type Risk int

const (
	Safe    Risk = iota // reads, or only touches local files
	Unknown             // a command (or a flag before it) the guard doesn't know
	Change              // changes the cluster
)

// The commands each tool has, plus a few well-known kubectl plugins that only
// read or touch local files. Any other verb is Unknown: a plugin, a typo, or
// a flag value the parser took for the verb.
var commands = map[string]map[string]bool{
	"kubectl": set("create", "expose", "run", "set", "explain", "get", "edit", "delete", "rollout", "scale",
		"autoscale", "certificate", "cluster-info", "top", "cordon", "uncordon", "drain", "taint", "describe",
		"logs", "attach", "exec", "port-forward", "proxy", "cp", "auth", "debug", "events", "diff", "apply",
		"patch", "replace", "wait", "kustomize", "label", "annotate", "completion", "api-resources",
		"api-versions", "config", "plugin", "version", "options", "help",
		"krew", "ctx", "ns", "oidc-login", "neat", "tree"),
	"helm": set("completion", "create", "dependency", "dep", "env", "get", "help", "history", "hist",
		"install", "lint", "list", "ls", "package", "plugin", "pull", "fetch", "push", "registry", "repo",
		"rollback", "search", "show", "inspect", "status", "template", "test", "uninstall", "delete", "del",
		"un", "upgrade", "verify", "version", "diff"),
	"flux": set("bootstrap", "build", "check", "completion", "create", "debug", "delete", "diff", "envsubst",
		"events", "export", "get", "help", "install", "logs", "migrate", "pull", "push", "reconcile", "resume",
		"stats", "suspend", "tag", "trace", "tree", "uninstall", "version"),
}

// verbs that change the cluster; for some the subcommand decides (readOnlySub)
var risky = map[string]map[string]bool{
	"kubectl": set("apply", "create", "delete", "edit", "patch", "replace", "scale", "drain",
		"cordon", "uncordon", "taint", "label", "annotate", "set", "autoscale", "expose", "run", "rollout",
		"debug", "cp", "exec", "attach", "certificate", "auth"),
	"helm": set("install", "upgrade", "uninstall", "delete", "del", "un", "rollback", "test"),
	"flux": set("suspend", "resume", "delete", "create", "uninstall", "install", "bootstrap", "reconcile", "migrate"),
}

// the subcommands of a risky kubectl verb that only read; any other
// (rollout restart, auth reconcile, or one the guard doesn't know) changes
var readOnlySub = map[string]map[string]bool{
	"rollout": set("status", "history"),
	"auth":    set("can-i", "whoami"),
}

// Classify says what the command does to the cluster. It fails closed: a verb
// it doesn't know, or one it can't be sure of because of an unknown flag
// before it, is Unknown, and on production that asks like a change.
func Classify(tool string, p Parsed) Risk {
	r := classify(tool, p)
	switch {
	case p.Alt != nil:
		r = max(r, classify(tool, *p.Alt))
	case p.unknownBefore > 1:
		r = max(r, Unknown)
	}
	return r
}

func classify(tool string, p Parsed) Risk {
	known := commands[tool]
	switch {
	case known == nil || p.dashesFirst:
		return Unknown
	case p.Verb == "": // bare `kubectl`: usage
		return Safe
	case !known[p.Verb]:
		return Unknown
	case p.Help || !risky[tool][p.Verb]:
		return Safe
	case tool == "kubectl" && readOnlySub[p.Verb][p.Sub]:
		return Safe
	case p.DryRun, tool == "flux" && p.Verb == "create" && p.Export:
		return Safe
	}
	return Change
}
