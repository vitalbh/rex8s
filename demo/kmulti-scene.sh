#!/bin/sh
# Drives the kmulti scene in the real Rex app (demo mode), for a screen
# recording: opens three demo clusters side by side, then kall types the
# same commands in all of them. Needs rex8s demo on and Remote Control on.
#
#   rex8s demo on "$PWD/demo" && demo/kmulti-scene.sh && rex8s demo off
set -eu
rex8s=${REX8S:-$HOME/.config/rex/rex8s/bin/rex8s}
json() { python3 -c "import json,sys; print($1)"; }

app=$(rex do -e 'return rex.call("client.list")' --json | json "next(c['client_id'] for c in json.load(sys.stdin)['clients'] if (c.get('info') or {}).get('kind')=='app')")
out=$(rex new rex8s-demo --json)
sid=$(printf '%s' "$out" | json "json.load(sys.stdin)['session_id']")
w0=$(printf '%s' "$out" | json "json.load(sys.stdin)['initial_windows'][0]['window_id']")
rex -C "$app" do session.select session_id="$sid"
sleep 2

# the side-by-side tab, as kmulti -n api opens it
win=$(rex -s "$sid" do k8s_multi --args '{"ctxs":"acme-prod-use,acme-prod-euw,acme-staging","ns":"api"}' --json | json "json.load(sys.stdin)['window_id']")
rex api call -s "$sid" session.close_window "{\"window_id\":\"$w0\"}" >/dev/null
rex -C "$app" do session.select session_id="$sid" window_id="$win"
sleep 3

first=$(rex api call -s "$sid" session.list_blocks '{}' | json "[b['block_id'] for b in json.load(sys.stdin)['blocks'] if b['window_id']=='$win'][0]")
kall() { REX_BLOCK=$first REX_SESSION=$sid "$rex8s" kall "$@" >/dev/null 2>&1; }

kall clear
sleep 1
kall kubectl get deploy
sleep 5
kall kubectl get pods -l app=worker
sleep 6
echo "$sid"
