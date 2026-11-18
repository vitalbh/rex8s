package main

import (
	"testing"
	"time"
)

func TestParseUnlockArgs(t *testing.T) {
	d, names, err := parseUnlockArgs([]string{"use", "1h", "euw"})
	if err != nil || d != time.Hour || len(names) != 2 || names[0] != "use" || names[1] != "euw" {
		t.Fatalf("d=%v names=%v err=%v", d, names, err)
	}
	if d, names, _ := parseUnlockArgs(nil); d != defaultUnlock || len(names) != 0 {
		t.Errorf("default: %v %v", d, names)
	}
	for _, bad := range []string{"0s", "13h", "-5m"} {
		if _, _, err := parseUnlockArgs([]string{bad}); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
