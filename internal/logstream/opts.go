// Package logstream follows Kubernetes logs and prints them: the lines of
// one pod or of every pod of a workload (restarting when a rollout changes
// the pods), with a grep that highlights its matches, the cluster URLs made
// clickable by a Linker, and long lines cut instead of stopping the stream.
//
// Its dependencies come in from the caller: the kube.Client that runs
// kubectl, the clusters (for the kauth hint), and the Linker maker.
package logstream

import (
	"fmt"
	"regexp"
	"strings"
)

// Opts are klogs' -g and -s, passed to the log panes as --grep=/--since=.
type Opts struct{ Grep, Since string }

// Flags turns the options back into a log pane's arguments.
func (o Opts) Flags() []string {
	var f []string
	if o.Grep != "" {
		f = append(f, "--grep="+o.Grep)
	}
	if o.Since != "" {
		f = append(f, "--since="+o.Since)
	}
	return f
}

// Compile returns the grep as a case-insensitive regexp, nil without one.
func (o Opts) Compile() (*regexp.Regexp, error) {
	if o.Grep == "" {
		return nil, nil
	}
	re, err := regexp.Compile("(?i)" + o.Grep)
	if err != nil {
		return nil, fmt.Errorf("-g %q: %v", o.Grep, err)
	}
	return re, nil
}

// Describe is the options for a header line: " · only /re/ · since 1h".
func (o Opts) Describe() string {
	s := ""
	if o.Grep != "" {
		s += " · only /" + o.Grep + "/"
	}
	if o.Since != "" {
		s += " · since " + o.Since
	}
	return s
}

// SplitFlags takes --grep=/--since= out of a log pane's arguments.
func SplitFlags(args []string) ([]string, Opts) {
	var o Opts
	var rest []string
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--grep="):
			o.Grep = strings.TrimPrefix(a, "--grep=")
		case strings.HasPrefix(a, "--since="):
			o.Since = strings.TrimPrefix(a, "--since=")
		default:
			rest = append(rest, a)
		}
	}
	return rest, o
}
