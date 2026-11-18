// Small text helpers shared by the commands.

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/vitalbh/rex8s/internal/tui"
)

// oneLine joins the lines and runs of spaces of s into single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// truncate cuts s to n runes, ending in … when cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// lastLogLine is the last non-empty line of s.
func lastLogLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// logTail keeps the last failLogLines lines, each cut to a sane width.
func logTail(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > failLogLines {
		lines = lines[len(lines)-failLogLines:]
	}
	for i, l := range lines {
		lines[i] = truncate(strings.ReplaceAll(l, "\t", "  "), 220)
	}
	return strings.Join(lines, "\n")
}

// matchWords reports whether every word of filter is in text, ignoring case:
// "grafana use" matches "use monitoring/grafana".
func matchWords(text, filter string) bool {
	t := strings.ToLower(text)
	for _, w := range strings.Fields(strings.ToLower(filter)) {
		if !strings.Contains(t, w) {
			return false
		}
	}
	return true
}

// shellSplit splits a command line like a POSIX shell would for quoting
// (single quotes, double quotes, backslash escapes). No expansion is done.
func shellSplit(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord, quote := false, rune(0)
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch {
			case r == '"':
				quote = 0
			case r == '\\' && i+1 < len(rs) && strings.ContainsRune(`"\$`+"`", rs[i+1]):
				i++
				cur.WriteRune(rs[i])
			default:
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == '\\' && i+1 < len(rs):
			i++
			cur.WriteRune(rs[i])
			inWord = true
		case r == ' ' || r == '\t':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, errors.New("unterminated quote in pasted command")
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

// tildePath writes a path under the home directory as ~/….
func tildePath(p string) string {
	if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, h+"/") {
		return "~" + p[len(h):]
	}
	return p
}

// nsOr is ns, or "default" when empty.
func nsOr(ns string) string {
	if ns == "" {
		return "default"
	}
	return ns
}

// clip cuts s to n bytes, ending in … when cut.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// firstNonEmpty is the first of ss that is not "".
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// listMore joins the first n items with " · " and says how many more there are.
func listMore(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, " · ")
	}
	return strings.Join(items[:n], " · ") + tui.Dim.Render(fmt.Sprintf(" · … %d more", len(items)-n))
}

// plural: "1 pod", "3 pods".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
