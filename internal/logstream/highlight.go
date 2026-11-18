package logstream

import (
	"regexp"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

var matchStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("214"))

// highlightCodes are the sequences that start and end a highlighted match,
// taken from matchStyle (both empty when the output has no colors).
var highlightCodes = sync.OnceValues(func() (on, off string) {
	on, off, ok := strings.Cut(matchStyle.Render("x"), "x")
	if !ok {
		return "", ""
	}
	return on, off
})

// escEnd returns where the escape sequence starting at s[i] (ESC) ends, and
// whether it is complete: a CSI ends at its final byte, an OSC/DCS/APC/PM/SOS
// at BEL or ESC \, anything else after its second byte.
func escEnd(s string, i int) (end int, complete bool) {
	if i+1 >= len(s) {
		return len(s), false
	}
	switch s[i+1] {
	case '[':
		for j := i + 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1, true
			}
		}
		return len(s), false
	case ']', 'P', '_', '^', 'X':
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1, true
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2, true
			}
		}
		return len(s), false
	}
	return i + 2, true
}

// segment is a piece of a line: visible text, or an escape sequence.
type segment struct {
	s   string
	esc bool
	off int // text: where it starts in the visible text
}

func segments(line string) (segs []segment, visible string) {
	var vis strings.Builder
	start := 0
	for i := 0; i < len(line); {
		if line[i] != 0x1b {
			i++
			continue
		}
		if start < i {
			segs = append(segs, segment{s: line[start:i], off: vis.Len()})
			vis.WriteString(line[start:i])
		}
		end, _ := escEnd(line, i)
		segs = append(segs, segment{s: line[i:end], esc: true})
		i, start = end, end
	}
	if start < len(line) {
		segs = append(segs, segment{s: line[start:], off: vis.Len()})
		vis.WriteString(line[start:])
	}
	return segs, vis.String()
}

// Visible is line without its escape sequences (colors, OSC 8 links): the
// text a reader sees, which is what a grep must look at.
func Visible(line string) string {
	if !strings.Contains(line, "\x1b") {
		return line
	}
	_, v := segments(line)
	return v
}

// highlight marks re's matches in the visible text of line only: the log's
// own colors and the OSC 8 links (their targets) stay as they are, a match
// that spans a color change is marked on both sides of it, and the log's
// color comes back after each match. Matches of zero length are ignored.
func highlight(line string, re *regexp.Regexp) string {
	on, off := highlightCodes()
	if re == nil || on == "" {
		return line
	}
	segs, vis := segments(line)
	var ms [][]int
	for _, m := range re.FindAllStringIndex(vis, -1) {
		if m[1] > m[0] {
			ms = append(ms, m)
		}
	}
	if len(ms) == 0 {
		return line
	}
	var b strings.Builder
	active := "" // the line's SGR state since its last reset
	mi := 0
	for _, sg := range segs {
		if sg.esc {
			b.WriteString(sg.s)
			if strings.HasPrefix(sg.s, "\x1b[") && strings.HasSuffix(sg.s, "m") {
				if sg.s == "\x1b[m" || sg.s == "\x1b[0m" {
					active = ""
				} else {
					active += sg.s
				}
			}
			continue
		}
		start, end := sg.off, sg.off+len(sg.s)
		pos := start
		for pos < end {
			for mi < len(ms) && ms[mi][1] <= pos {
				mi++
			}
			if mi == len(ms) || ms[mi][0] >= end {
				break
			}
			from, to := max(ms[mi][0], pos), min(ms[mi][1], end)
			b.WriteString(vis[pos:from])
			b.WriteString(on + vis[from:to] + off + active)
			pos = to
		}
		b.WriteString(vis[pos:end])
	}
	return b.String()
}

// grepLine: with no pattern every line passes; otherwise only lines whose
// visible text matches, with the matches highlighted.
func grepLine(line string, re *regexp.Regexp) (string, bool) {
	if re == nil {
		return line, true
	}
	if !re.MatchString(Visible(line)) {
		return "", false
	}
	return highlight(line, re), true
}
