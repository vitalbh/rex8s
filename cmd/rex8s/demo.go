package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vitalbh/rex8s/internal/demo"
)

// demoSwitch: rex8s demo on DIR | off | status
func demoSwitch(args []string) error {
	cmd := "status"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "on":
		if len(args) < 2 {
			return errors.New("usage: rex8s demo on DIR (the repo's demo/ directory)")
		}
		dir, err := filepath.Abs(args[1])
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dir, "kubeconfig")); err != nil {
			return fmt.Errorf("%s doesn't look like the demo directory (no kubeconfig)", dir)
		}
		if err := os.MkdirAll(filepath.Dir(demo.Marker()), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(demo.Marker(), []byte(dir+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Println("demo mode ON: Rex panes, Raycast and k* commands now see only fake data from " + dir)
		fmt.Println("open new tabs to use it; turn it off with: rex8s demo off")
	case "off":
		if err := os.Remove(demo.Marker()); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("demo mode OFF")
	default:
		if d := demo.Dir(); d != "" {
			fmt.Println("demo mode ON: " + d)
		} else {
			fmt.Println("demo mode off")
		}
	}
	return nil
}
