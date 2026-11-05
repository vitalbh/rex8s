package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestPaneRecords(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("REX_BLOCK", "block:abc")
	saveRecord(paneRecord{Block: "block:abc", Context: "acme-prod-use", Short: "use", Layout: true, Opened: time.Now()})
	updateRecord("acme-staging", "staging", "api") // kctx + kns in that pane
	r, ok := loadRecord("abc")
	if !ok || r.Context != "acme-staging" || r.NS != "api" || !r.Layout {
		t.Fatalf("record = %+v ok=%v", r, ok)
	}
	if got := restoreLabel([]paneRecord{r}); !strings.Contains(got, "staging/api") {
		t.Fatalf("label = %q", got)
	}
	forgetOnClose("block:abc", false) // the user typed exit
	if _, err := os.Stat(recordPath("abc")); !os.IsNotExist(err) {
		t.Fatal("record must be deleted when the shell exits normally")
	}
}
