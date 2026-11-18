package main

import "testing"

func TestRemoteForwardHint(t *testing.T) {
	oldRemote, oldName := remoteRex, serverName
	t.Cleanup(func() { remoteRex, serverName = oldRemote, oldName })
	serverName = func() string { return "devbox" }

	remoteRex = func() bool { return false }
	if h := remoteForwardHint(54321); h != "" {
		t.Errorf("local session: %q", h)
	}
	remoteRex = func() bool { return true }
	want := "on the Mac: rex -S devbox forward new -L 54321:127.0.0.1:54321  (or Forward Port… in Rex's palette)"
	if h := remoteForwardHint(54321); h != want {
		t.Errorf("remote: %q", h)
	}
}
