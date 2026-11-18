package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The audit log: one line per decision about a production change, in
// ~/.cache/rex8s/audit.log (or $XDG_CACHE_HOME/rex8s/audit.log):
//
//	2026-11-17T10:04:05-03:00	confirmed	use	kubectl delete pod web-1
//	2026-11-17T10:05:12-03:00	refused	use,euw	kroll: rollout restart 2 workload(s) (agent)
//
// Tab-separated: time, outcome (confirmed, cancelled, refused, forced), the
// production clusters, the command. Past auditMax bytes the file moves to
// audit.log.1 (replacing the previous one), so it never holds more than
// about twice that. Writing is best effort: a failure never blocks a command.

const auditMax = 512 << 10

var now = time.Now // tests pin it

// AuditPath is where the audit log lives.
func AuditPath() string {
	dir := sys.getenv("XDG_CACHE_HOME")
	if dir == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(h, ".cache")
	}
	return filepath.Join(dir, "rex8s", "audit.log")
}

func audit(clusters, outcome, command string) {
	path := AuditPath()
	if path == "" {
		return
	}
	if sys.getenv("REX8S_AGENT") != "" || sys.getenv("CLAUDECODE") == "1" {
		command += " (agent)"
	}
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")
	line := fmt.Sprintf("%s\t%s\t%s\t%s\n", now().Format(time.RFC3339), outcome,
		clean.Replace(clusters), clean.Replace(command))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if fi, err := os.Stat(path); err == nil && fi.Size()+int64(len(line)) > auditMax {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(line)
}
