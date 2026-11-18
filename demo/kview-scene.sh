#!/bin/sh
# Drives ~40s of kview in the real Rex app (demo mode), for a screen
# recording: ⇧⌘V opens kview on every cluster → a failing pod → why, logs
# and a shell open in Rex splits beside it → describe → workloads.
#
#   rex8s demo on "$PWD/demo" && demo/kview-scene.sh && rex8s demo off
set -eu
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
app=$(rex do -e 'return rex.call("client.list")' --json | json "next(c['client_id'] for c in d['clients'] if (c.get('info') or {}).get('kind')=='app')")
out=$(rex new rex8s-kview --json)
sid=$(printf '%s' "$out" | json "d['session_id']")
w0=$(printf '%s' "$out" | json "d['initial_windows'][0]['window_id']")
w1=$(rex -s "$sid" do k8s_view_all --json | json "d['window_id']")
rex -C "$app" do session.select session_id="$sid" window_id="$w1"
rex api call -s "$sid" session.close_window "{\"window_id\":\"$w0\"}" >/dev/null
b=$(rex api call -s "$sid" session.list_blocks '{}' | json "[x['block_id'] for x in d['blocks'] if x['window_id']=='$w1'][0]")
key() { rex send-key -s "$sid" -b "$b" "$@"; }
typ() { rex send -s "$sid" -b "$b" "$1"; }
# a window of its own without the sidebar (your other sessions stay out of
# the recording); resize it, start recording it, then press enter here
rex -C "$app" do client.tab.move_to_new_window >/dev/null 2>&1 || true
sleep 0.5
rex -C "$app" do client.sidebar.toggle >/dev/null 2>&1 || true
if [ -z "${KVIEW_SCENE_GO:-}" ]; then
  printf 'resize the window, start recording it, then press enter… '
  read -r _
else
  while [ ! -e "$KVIEW_SCENE_GO" ]; do sleep 0.3; done
fi
key R; sleep 3.5
key '[Slash]'; sleep 0.4; typ "worker"; sleep 1.2; key enter; sleep 1.5
key w; sleep 6                      # why: Claude's answer in a split
key l; sleep 5                      # logs: another split, kview keeps the focus
key s; sleep 4                      # shell in the pod: a third split, focused
# (nothing typed in it: the demo shell runs on this machine)
rex -C "$app" do pane.focus direction=left >/dev/null 2>&1 || true; sleep 1.2
key d; sleep 3; key q; sleep 1      # describe in kview's viewer
key escape; sleep 0.6; key tab; sleep 3.5
