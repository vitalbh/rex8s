package guard

import (
	"errors"
	"strings"
	"testing"

	"github.com/vitalbh/rex8s/internal/config"
)

// The Rex palette asks when nobody is at a terminal.
func TestPalette(t *testing.T) {
	off := false
	noPalette := &config.Config{Clusters: testCfg.Clusters, GuardPalette: &off}
	cases := []struct {
		name    string
		env     map[string]string
		tty     bool
		cfg     *config.Config
		allowed bool
		reason  string
		err     error // what the palette returns
		asked   bool  // the palette was asked
		ran     bool
		want    error
		audit   string
		msg     string // in the error
	}{
		{"allowed", map[string]string{"CLAUDECODE": "1"}, true, testCfg, true, "", nil, true, true, nil, "approved\tuse", ""},
		{"denied", map[string]string{"CLAUDECODE": "1"}, true, testCfg, false, "deny", nil, true, false, ErrRefused, "denied\tuse", "denied it in the Rex palette"},
		{"timeout", nil, false, testCfg, false, "timeout", nil, true, false, ErrRefused, "refused\tuse", "asked in the Rex palette: timeout"},
		{"no Rex", map[string]string{"REX8S_AGENT": "1"}, true, testCfg, false, "", errNoPalette, true, false, ErrRefused, "refused\tuse", "REX8S_FORCE=1"},
		{"guard_palette false", map[string]string{"CLAUDECODE": "1"}, true, noPalette, true, "", nil, false, false, ErrRefused, "refused\tuse", "REX8S_FORCE=1"},
		{"person at the terminal: not asked", nil, true, testCfg, true, "", nil, false, true, nil, "confirmed\tuse", ""},
		{"forced: not asked", map[string]string{"REX8S_FORCE": "1", "CLAUDECODE": "1"}, false, testCfg, false, "deny", nil, false, true, nil, "forced\tuse", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{env: c.env, tty: c.tty, answer: "use"}
			log := setup(t, f)
			asked := false
			var gotWhat string
			sys.palette = func(prod []string, what string, maybe bool, _ func() string) (bool, string, error) {
				asked, gotWhat = true, what
				return c.allowed, c.reason, c.err
			}
			err := run("kubectl", "/fake/kubectl", []string{"--context", "use", "delete", "pod", "x"}, c.cfg)
			if !errors.Is(err, c.want) || (c.want == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if c.msg != "" && !strings.Contains(err.Error(), c.msg) {
				t.Errorf("message %q lacks %q", err, c.msg)
			}
			if asked != c.asked {
				t.Errorf("palette asked = %v, want %v", asked, c.asked)
			}
			if asked && gotWhat != "kubectl --context use delete pod x" {
				t.Errorf("palette what = %q", gotWhat)
			}
			if (f.ran != nil) != c.ran {
				t.Errorf("ran = %v, want %v", f.ran != nil, c.ran)
			}
			if l := auditLines(t, log); len(l) != 1 || !strings.Contains(l[0], "\t"+c.audit+"\t") {
				t.Errorf("audit = %q, want %q", l, c.audit)
			}
		})
	}
}
