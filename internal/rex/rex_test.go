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

func TestStatusSeq(t *testing.T) {
	for _, c := range []struct{ id, state, kind, title, msg, want string }{
		{"", "clear", "", "", "", "\x1b]7501;state=clear:app=rex8s\x1b\\"},
		{"", "idle", "", "", "following 3 pods", "\x1b]7501;state=idle:app=rex8s:msg=Zm9sbG93aW5nIDMgcG9kcw==\x1b\\"},
		{"", "blocked", "auth", "", "x", "\x1b]7501;state=blocked:app=rex8s:kind=auth:msg=eA==\x1b\\"},
		{"c1", "error", "auth", "use", "", "\x1b]7501;state=error:app=rex8s:id=c1:title=dXNl\x1b\\"}, // kind only with blocked
	} {
		if got := statusSeq(c.id, c.state, c.kind, c.title, c.msg); got != c.want {
			t.Errorf("statusSeq(%q, %q, %q) = %q, want %q", c.id, c.state, c.kind, got, c.want)
		}
	}
}
