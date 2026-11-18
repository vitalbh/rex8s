package logstream

import (
	"regexp"
	"strings"
	"testing"
)

// Tests run without a terminal, where lipgloss renders no colors: mark the
// highlight with visible brackets instead.
func init() {
	highlightCodes = func() (string, string) { return "<", ">" }
}

func TestHighlightPlain(t *testing.T) {
	re := regexp.MustCompile("(?i)time ?out")
	if got := highlight("upstream TIMEOUT after 30s, timeout", re); got != "upstream <TIMEOUT> after 30s, <timeout>" {
		t.Fatalf("got %q", got)
	}
	if got := highlight("x", nil); got != "x" {
		t.Fatalf("nil re: %q", got)
	}
}

// The log's own colors: a pattern that matches inside "\x1b[31m" must not
// touch it, and the color comes back after each match (B20).
func TestHighlightSkipsANSI(t *testing.T) {
	line := "\x1b[31mERROR\x1b[0m took 31ms"
	for _, c := range []struct{ re, want string }{
		{"31", "\x1b[31mERROR\x1b[0m took <31>ms"},
		{"m", "\x1b[31mERROR\x1b[0m took 31<m>s"},
		{`\d+`, "\x1b[31mERROR\x1b[0m took <31>ms"},
		{"err", "\x1b[31m<ERR>\x1b[31mOR\x1b[0m took 31ms"}, // inside the red: red again after
		{"or took", "\x1b[31mERR<OR>\x1b[31m\x1b[0m< took> 31ms"},
	} {
		re := regexp.MustCompile("(?i)" + c.re)
		if got := highlight(line, re); got != c.want {
			t.Errorf("/%s/:\n got %q\nwant %q", c.re, got, c.want)
		}
		if strings.NewReplacer("<", "", ">", "").Replace(Visible(highlight(line, re))) != Visible(line) {
			t.Errorf("/%s/ changed the visible text", c.re)
		}
	}
}

// A regexp that matches the empty string marks nothing (it used to insert
// a style at every position).
func TestHighlightEmptyMatch(t *testing.T) {
	line := "\x1b[32mok\x1b[0m bbb"
	if got := highlight(line, regexp.MustCompile("a*")); got != line {
		t.Fatalf("a*: %q", got)
	}
	if got := highlight("baab", regexp.MustCompile("a*")); got != "b<aa>b" {
		t.Fatalf("a* on baab: %q", got)
	}
}

func TestHighlightKeepsLinkTargets(t *testing.T) {
	link := "\x1b]8;;http://localhost:4100/login\x1b\\http://grafana.monitoring.svc:3000/login\x1b]8;;\x1b\\"
	line := "upstream " + link + " ok"
	got := highlight(line, regexp.MustCompile("(?i)grafana|login"))
	osc := regexp.MustCompile("\x1b\\]8;;([^\x1b]*)\x1b\\\\")
	ts := osc.FindAllStringSubmatch(got, -1)
	if len(ts) != 2 || ts[0][1] != "http://localhost:4100/login" || ts[1][1] != "" {
		t.Fatalf("link targets changed: %q", got)
	}
	if !strings.Contains(got, "http://<grafana>.monitoring.svc:3000/<login>") {
		t.Fatalf("link text not highlighted: %q", got)
	}
}

func TestVisible(t *testing.T) {
	if got := Visible("\x1b[1;31mred\x1b[0m \x1b]8;;http://x\x1b\\link\x1b]8;;\x07 end"); got != "red link end" {
		t.Fatalf("got %q", got)
	}
}

func TestGrepLine(t *testing.T) {
	re, err := Opts{Grep: "time ?out"}.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := grepLine("GET /health 200", re); ok {
		t.Error("non-matching line kept")
	}
	if l, ok := grepLine("upstream TIMEOUT after 30s", re); !ok || !strings.Contains(l, "<TIMEOUT>") {
		t.Errorf("match = %q %v", l, ok)
	}
	if l, ok := grepLine("x", nil); !ok || l != "x" {
		t.Error("no pattern should pass everything")
	}
	// only the color code has "31": not a match
	if _, ok := grepLine("\x1b[31mfailed\x1b[0m", regexp.MustCompile("31")); ok {
		t.Error("matched inside a color code")
	}
}

func TestOpts(t *testing.T) {
	rest, o := SplitFlags([]string{"--grep=err", "ctx", "--since=1h", "ns", "pod"})
	if strings.Join(rest, " ") != "ctx ns pod" || o.Grep != "err" || o.Since != "1h" {
		t.Errorf("split = %v %+v", rest, o)
	}
	if strings.Join(o.Flags(), " ") != "--grep=err --since=1h" {
		t.Errorf("flags = %v", o.Flags())
	}
	if o.Describe() != " · only /err/ · since 1h" {
		t.Errorf("describe = %q", o.Describe())
	}
	if _, err := (Opts{Grep: "("}).Compile(); err == nil {
		t.Error("bad regex accepted")
	}
	if re, err := (Opts{}).Compile(); re != nil || err != nil {
		t.Error("no grep should compile to nil")
	}
}
