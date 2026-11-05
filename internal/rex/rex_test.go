package rex

import "testing"

func TestThemeSeq(t *testing.T) {
	for in, want := range map[string]string{
		"":        "\x1b]111\x07",
		"#2b1216": "\x1b]11;#2b1216\x07",
		"2b1216":  "\x1b]11;#2b1216\x07",
	} {
		if got := themeSeq(in); got != want {
			t.Errorf("themeSeq(%q) = %q, want %q", in, got, want)
		}
	}
}
