package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vitalbh/rex8s/internal/config"
	"github.com/vitalbh/rex8s/internal/yamledit"
)

func TestEditConfigRefusesConcurrentChange(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.yaml"
	os.WriteFile(path, []byte("ai_language: en\n"), 0o644)
	t.Setenv("REX8S_CONFIG", path)
	cfg, _ := config.Load()
	a := &app{cfg: cfg}
	if err := a.editConfig(func(doc string) string { return yamledit.SetKey(doc, "ai_language", "pt-BR") }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "ai_language: pt-BR\n" {
		t.Errorf("file = %q", b)
	}
	// the user saves the file while rex8s is editing it
	err := a.editConfig(func(doc string) string {
		future := time.Now().Add(time.Hour)
		os.Chtimes(path, future, future)
		return yamledit.SetKey(doc, "ai_language", "en")
	})
	if err == nil || !strings.Contains(err.Error(), "changed while") {
		t.Errorf("concurrent edit not refused: %v", err)
	}
	// a change that breaks the YAML is undone
	before, _ := os.ReadFile(path)
	if err := a.editConfig(func(doc string) string { return doc + "clusters: [ {\n" }); err == nil {
		t.Error("broken YAML accepted")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Errorf("not restored: %q", after)
	}
}

func TestConfigSetForwardExpire(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	os.WriteFile(path, []byte("ai_language: en\n"), 0o644)
	t.Setenv("REX8S_CONFIG", path)
	cfg, _ := config.Load()
	a := &app{cfg: cfg}
	if err := a.configCmd([]string{"set", "forward_expire", "2d"}); err == nil {
		t.Error("forward_expire 2d accepted")
	}
	if err := a.configCmd([]string{"set", "forward_expire", "off"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), `forward_expire: "off"`) {
		t.Errorf("file = %q", b)
	}
	if d := a.cfg.ForwardExpiry(); d != 0 {
		t.Errorf("expiry after set off = %v", d)
	}
}

// kview_watch is watch's old name: config takes it for watch, writing
// watch removes it, and get watch reads it while it is the one written.
func TestConfigWatchOldName(t *testing.T) {
	path := t.TempDir() + "/config.yaml"
	os.WriteFile(path, []byte("kview_watch: false\n"), 0o644)
	t.Setenv("REX8S_CONFIG", path)
	cfg, _ := config.Load()
	a := &app{cfg: cfg}
	if v := a.configValue("watch"); v != "false" {
		t.Errorf("get watch = %q", v)
	}
	if err := a.configCmd([]string{"set", "kview_watch", "true"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "watch: true\n" {
		t.Errorf("file = %q", b)
	}
	if !a.cfg.WatchOn() {
		t.Error("watch off after set true")
	}
	os.WriteFile(path, []byte("kview_watch: false\n"), 0o644)
	if err := a.configCmd([]string{"unset", "watch"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), "watch") {
		t.Errorf("file = %q", b)
	}
}
