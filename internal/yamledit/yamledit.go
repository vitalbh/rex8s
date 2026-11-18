// Package yamledit changes a YAML file as text, so its comments, order and
// layout stay as the user wrote them. It knows the shapes rex8s's config
// uses: top-level "key: value" lines, block lists ("  - item") and inline
// lists ("key: [ a, b ]").
package yamledit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// reserved: plain scalars YAML (1.1, which sigs.k8s.io/yaml follows) reads as
// something other than a string.
var reserved = map[string]bool{
	"y": true, "n": true, "yes": true, "no": true, "on": true, "off": true,
	"true": true, "false": true, "null": true, "~": true,
	".inf": true, "-.inf": true, "+.inf": true, ".nan": true,
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{1,2}-\d{1,2}`)

// String returns s written as a YAML string: plain when YAML reads it back
// as the same string, quoted otherwise (booleans such as off/on/yes/no,
// null and ~, numbers, dates, and text with YAML punctuation).
func String(s string) string {
	if needsQuotes(s) {
		return strconv.Quote(s)
	}
	return s
}

func needsQuotes(s string) bool {
	switch {
	case s == "", strings.TrimSpace(s) != s:
		return true
	case strings.ContainsAny(s, ":#{}[],&*!|>'\"%@`\n\t"):
		return true
	case strings.HasPrefix(s, "-"), strings.HasPrefix(s, "?"):
		return true
	case reserved[strings.ToLower(s)]:
		return true
	case dateRe.MatchString(s):
		return true
	}
	t := strings.ReplaceAll(s, "_", "")
	if _, err := strconv.ParseFloat(t, 64); err == nil {
		return true
	}
	if _, err := strconv.ParseInt(t, 0, 64); err == nil {
		return true
	}
	return false
}

// Comment returns the "# …" of a line, outside quotes ("" when none).
func Comment(l string) string {
	var quote rune
	for i, r := range l {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#' && i > 0 && (l[i-1] == ' ' || l[i-1] == '\t'):
			return l[i:]
		}
	}
	return ""
}

// withoutComment: the line without its comment, trimmed.
func withoutComment(l string) string {
	if c := Comment(l); c != "" {
		l = strings.TrimSuffix(l, c)
	}
	return strings.TrimSpace(l)
}

// SetKey replaces a top-level "key: …" line (keeping its comment), or
// appends one. value is written as given: quote it with String when needed.
func SetKey(doc, key, value string) string {
	lines := strings.Split(doc, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, key+":") {
			continue
		}
		line := key + ": " + value
		if c := Comment(l); c != "" {
			line = fmt.Sprintf("%-42s %s", line, c)
		}
		lines[i] = line
		return strings.Join(lines, "\n")
	}
	if doc != "" && !strings.HasSuffix(doc, "\n") {
		doc += "\n"
	}
	return doc + key + ": " + value + "\n"
}

// RemoveKey removes a top-level key with its block (list or map) under it.
func RemoveKey(doc, key string) string {
	lines := strings.Split(doc, "\n")
	var out []string
	skipping := false
	for _, l := range lines {
		if strings.HasPrefix(l, key+":") {
			skipping = true
			continue
		}
		if skipping && (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "-")) {
			continue // the key's block list or map
		}
		skipping = false
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// splitFlow splits the inside of an inline list at the commas that separate
// its items (not the ones inside {…}, […] or quotes).
func splitFlow(inner string) []string {
	var out []string
	depth, start := 0, 0
	var quote rune
	for i, r := range inner {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '{' || r == '[':
			depth++
		case r == '}' || r == ']':
			depth--
		case r == ',' && depth == 0:
			out = append(out, inner[start:i])
			start = i + 1
		}
	}
	out = append(out, inner[start:])
	items := out[:0]
	for _, it := range out {
		if it = strings.TrimSpace(it); it != "" {
			items = append(items, it)
		}
	}
	return items
}

// flowEnd returns the index just past the "]" that closes the "[" at
// text[0], or -1 when it is not closed.
func flowEnd(text string) int {
	depth := 0
	var quote rune
	for i, r := range text {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '{' || r == '[':
			depth++
		case r == '}' || r == ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// AddListItem appends "  - item" to the top-level list key, keeping the
// rest of the file (and its comments) as it is. Handles a missing key, an
// empty inline list (key: []), a block list, and an inline list with items
// (key: [ a, b ], also over several lines), which becomes a block list.
func AddListItem(doc, key, item string) string {
	lines := strings.Split(strings.TrimRight(doc, "\n"), "\n")
	if doc == "" {
		lines = nil
	}
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, key+":") {
			start = i
			break
		}
	}
	entry := "  - " + item
	if start < 0 {
		return strings.Join(append(lines, key+":", entry), "\n") + "\n"
	}
	comment := Comment(lines[start])
	rest := withoutComment(strings.TrimPrefix(lines[start], key+":"))
	if rest == "[]" || rest == "" {
		if rest == "[]" {
			lines[start] = key + ":"
			if comment != "" {
				lines[start] = fmt.Sprintf("%-42s %s", key+":", comment)
			}
		}
		end := start
		for i := start + 1; i < len(lines); i++ {
			l := lines[i]
			if strings.TrimSpace(l) == "" || strings.HasPrefix(l, " ") || strings.HasPrefix(l, "-") || strings.HasPrefix(l, "#") {
				if strings.TrimSpace(l) != "" && !strings.HasPrefix(strings.TrimSpace(l), "#") {
					end = i
				}
				continue
			}
			break
		}
		out := append([]string{}, lines[:end+1]...)
		out = append(out, entry)
		out = append(out, lines[end+1:]...)
		return strings.Join(out, "\n") + "\n"
	}

	// an inline list with items: rewrite it as a block list (a block list
	// after it would repeat the key)
	end, text := start, rest
	if strings.HasPrefix(rest, "[") {
		for flowEnd(text) < 0 && end+1 < len(lines) {
			end++
			text += " " + withoutComment(lines[end])
		}
	}
	var items []string
	if n := flowEnd(text); strings.HasPrefix(text, "[") && n > 0 {
		items = splitFlow(text[1 : n-1])
	} else {
		items = []string{rest} // a single value: becomes the first item
	}
	head := key + ":"
	if comment != "" {
		head = fmt.Sprintf("%-42s %s", head, comment)
	}
	out := append([]string{}, lines[:start]...)
	out = append(out, head)
	for _, it := range items {
		out = append(out, "  - "+it)
	}
	out = append(out, entry)
	out = append(out, lines[end+1:]...)
	return strings.Join(out, "\n") + "\n"
}

var flowLine = func(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `:\s*\[(.*?)\](.*)$`)
}

func unquote(s string) string { return strings.Trim(strings.TrimSpace(s), `"'`) }

// AddFlowItem adds value to a list written either way, "key: [a, b]" or a
// block list; nothing changes when it is already there.
func AddFlowItem(doc, key, value string) string {
	if m := flowLine(key).FindStringSubmatchIndex(doc); m != nil {
		inner := strings.TrimSpace(doc[m[2]:m[3]])
		for _, it := range splitFlow(inner) {
			if unquote(it) == unquote(value) {
				return doc
			}
		}
		if inner != "" {
			inner += ", "
		}
		return doc[:m[0]] + key + ": [ " + strings.TrimSpace(inner+value) + " ]" + doc[m[4]:m[5]] + doc[m[1]:]
	}
	if strings.Contains(doc, "\n  - "+value+"\n") {
		return doc
	}
	return AddListItem(doc, key, value)
}

// RemoveListItem removes value from a list written either way.
func RemoveListItem(doc, key, value string) string {
	if m := flowLine(key).FindStringSubmatchIndex(doc); m != nil {
		var keep []string
		for _, it := range splitFlow(doc[m[2]:m[3]]) {
			if unquote(it) != value {
				keep = append(keep, it)
			}
		}
		return doc[:m[0]] + key + ": [ " + strings.Join(keep, ", ") + " ]" + doc[m[4]:m[5]] + doc[m[1]:]
	}
	var out []string
	in := false
	for _, l := range strings.Split(doc, "\n") {
		if strings.HasPrefix(l, key+":") {
			in = true
		} else if in && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "-") && strings.TrimSpace(l) != "" {
			in = false
		}
		if in && strings.HasPrefix(strings.TrimSpace(l), "-") {
			t := withoutComment(strings.TrimPrefix(strings.TrimSpace(l), "-"))
			if unquote(t) == value {
				continue
			}
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// ErrChanged: the file changed on disk while it was being edited.
var ErrChanged = errors.New("the file changed while it was being edited")

// Result says what Edit did.
type Result struct {
	Path    string
	Changed bool // false: change left the text as it was, nothing was written
}

// Edit is the one way rex8s writes a YAML file: read it, change the text,
// refuse (ErrChanged) when the file changed meanwhile, write it atomically
// (a temporary file renamed over it), then run check (e.g. load the
// config); when check fails, the file is put back as it was. A missing file
// starts empty. Printing is left to the caller.
func Edit(path string, change func(doc string) string, check func() error) (Result, error) {
	res := Result{Path: path}
	before, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return res, err
	}
	st, _ := os.Stat(path)
	after := change(string(before))
	if after == string(before) {
		return res, nil
	}
	if st2, _ := os.Stat(path); (st == nil) != (st2 == nil) || (st != nil && !st.ModTime().Equal(st2.ModTime())) {
		return res, ErrChanged
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return res, err
	}
	if err := writeAtomic(path, []byte(after)); err != nil {
		return res, err
	}
	if check != nil {
		if err := check(); err != nil {
			if existed {
				_ = writeAtomic(path, before)
			} else {
				_ = os.Remove(path)
			}
			return res, fmt.Errorf("the change broke the file, so it was undone: %v", err)
		}
	}
	res.Changed = true
	return res, nil
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
