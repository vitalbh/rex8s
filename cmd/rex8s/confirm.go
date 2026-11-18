package main

import (
	"errors"

	"github.com/vitalbh/rex8s/internal/guard"
	"github.com/vitalbh/rex8s/internal/kube"
)

// confirmChange applies the production policy (guard.ConfirmChange) to a
// change a rex8s command makes itself. ok=false with a nil error: the user
// said no ("cancelled." is already printed); with an error: refused (no
// terminal, or an agent), and the error says how to go on.
func confirmChange(clusters []kube.Context, what string, notes ...string) (ok bool, err error) {
	err = guard.ConfirmChange(clusters, what, notes...)
	if errors.Is(err, guard.ErrCancelled) {
		return false, nil
	}
	return err == nil, err
}
