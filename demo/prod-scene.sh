#!/bin/sh
# Sets up the README's production screenshot (docs/rex-prod.png) in the real
# Rex app, demo mode: Cmd+Shift+K on the production cluster use, namespace
# api — kview on the left, the cluster's shell on top right, the
# namespace's warnings below. Then take the
# screenshot (Cmd+Shift+4, space, click the Rex window) and remove the
# session it prints.
#
#   rex8s demo on "$PWD/demo" && demo/prod-scene.sh
#   rex rm SESSION && rex8s demo off
set -eu
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
blocks() { rex api call -s "$sid" session.list_blocks '{}'; }
typ() { rex send -s "$sid" -b "$1" "$2"; }
CR=$(printf '\r')

app=$(rex do -e 'return rex.call("client.list")' --json | json "next(c['client_id'] for c in d['clients'] if (c.get('info') or {}).get('kind')=='app')")
out=$(rex new rex8s-prod --json)
sid=$(printf '%s' "$out" | json "d['session_id']")
w0=$(printf '%s' "$out" | json "d['initial_windows'][0]['window_id']")
show() { rex -C "$app" do session.select session_id="$sid" window_id="$1"; }

# Cmd+Shift+K: the picker opens in a new tab; pick use, then api
w=$(rex -s "$sid" do k8s_pick_layout --json | json "d['window_id']")
show "$w"
rex api call -s "$sid" session.close_window "{\"window_id\":\"$w0\"}" >/dev/null
b=$(blocks | json "[x['block_id'] for x in d['blocks'] if x['window_id']=='$w'][0]")
sleep 2
typ "$b" "use"; sleep 1.2; typ "$b" "$CR"
sleep 2; typ "$b" "api"; sleep 1.2; typ "$b" "$CR"
sleep 4

echo "$sid"
