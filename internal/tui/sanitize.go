package tui

import "strings"

// Clean makes text that came from a cluster (event messages, describe
// output, YAML, logs shown in a viewer) safe to print: escape sequences and
// control characters are dropped, so an app can't clear the screen, change
// the title or set the clipboard (OSC 52) through its own output. Tabs and
// newlines stay.
func Clean(s string) string {
	if !needsClean(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == 0x1b: // ESC: skip the whole sequence
			i = skipEscape(rs, i)
		case r == 0x9b || r == 0x9d || r == 0x90: // C1 CSI, OSC, DCS
			i = skipString(rs, i+1, r == 0x9b)
		case r == '\t' || r == '\n':
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			// other control characters: dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func needsClean(s string) bool {
	for _, r := range s {
		if (r < 0x20 && r != '\t' && r != '\n') || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return true
		}
	}
	return false
}

// skipEscape returns the index of the last rune of the escape sequence that
// starts at rs[i] (ESC).
func skipEscape(rs []rune, i int) int {
	if i+1 >= len(rs) {
		return i
	}
	switch rs[i+1] {
	case '[': // CSI: parameters, then a final byte 0x40–0x7e
		return skipString(rs, i+2, true)
	case ']', 'P', '_', '^', 'X': // OSC, DCS, APC, PM, SOS: until BEL or ST
		return skipString(rs, i+2, false)
	default: // two-character sequence
		return i + 1
	}
}

// skipString skips a CSI (csi: until a final byte) or a string sequence
// (until BEL or ESC \) starting at rs[j]; it returns the index of its last
// rune.
func skipString(rs []rune, j int, csi bool) int {
	for ; j < len(rs); j++ {
		r := rs[j]
		if csi {
			if r >= 0x40 && r <= 0x7e {
				return j
			}
			continue
		}
		if r == 0x07 || r == 0x9c {
			return j
		}
		if r == 0x1b && j+1 < len(rs) && rs[j+1] == '\\' {
			return j + 1
		}
	}
	return len(rs) - 1
}
