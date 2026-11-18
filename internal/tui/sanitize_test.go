package tui

import "testing"

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"plain text":                              "plain text",
		"tab\tand\nnewline":                       "tab\tand\nnewline",
		"\x1b[2J\x1b[Hcleared":                    "cleared",
		"\x1b[31mred\x1b[0m":                      "red",
		"\x1b]0;title\x07after":                   "after",
		"\x1b]52;c;aGk=\x1b\\clip":                "clip",
		"bell\x07 and cr\r":                       "bell and cr",
		"c1 \u009b31mcsi":                         "c1 csi",
		"Readiness probe failed: \x1b]8;;x\x1b\\": "Readiness probe failed: ",
		"ünïcode ✓":                               "ünïcode ✓",
		"trailing esc \x1b":                       "trailing esc ",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}
