package guard

import (
	"os"
	"path/filepath"
	"strings"
)

// PaneDrift is a warning when this runs inside a rex8s pane (REX8S=1) whose
// KUBECONFIG no longer starts with the pane's own file: a shell rc that
// exports KUBECONFIG again drops it, and kubectl then talks to the
// kubeconfig's current context, whatever the pane's label and tint say.
// "" when all is well (or outside a pane).
func PaneDrift() string {
	if sys.getenv("REX8S") != "1" {
		return ""
	}
	audit := AuditPath()
	if audit == "" {
		return ""
	}
	dir := filepath.Dir(audit) + string(os.PathSeparator)
	if first := filepath.SplitList(sys.getenv("KUBECONFIG")); len(first) > 0 && strings.HasPrefix(first[0], dir) {
		return ""
	}
	return "rex8s: KUBECONFIG was set again after this pane opened (a shell rc?), so this uses the kubeconfig's " +
		"current context, not the pane's cluster. Export it only outside panes: [ -z \"$REX8S\" ] && export KUBECONFIG=… " +
		"(rex8s doctor shows where)"
}
