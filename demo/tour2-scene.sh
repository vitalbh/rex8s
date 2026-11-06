#!/bin/sh
# Second short tour (demo mode, real Rex app): in a production pane, klogs
# opens a workload's merged logs in a split without taking the focus, then
# kroll restarts it with the progress in the tab header (OSC 7501).
#
#   rex8s demo on "$PWD/demo" && demo/tour2-scene.sh && rex8s demo off
set -eu
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
typ() { rex send -s "$sid" -b "$1" "$2"; }
CR=$(printf '\r')

app=$(rex do -e 'return rex.call("client.list")' --json | json "next(c['client_id'] for c in d['clients'] if (c.get('info') or {}).get('kind')=='app')")
out=$(rex new rex8s-demo --json)
sid=$(printf '%s' "$out" | json "d['session_id']")
w0=$(printf '%s' "$out" | json "d['initial_windows'][0]['window_id']")
w=$(rex -s "$sid" do k8s_tab --args '{"ctx":"acme-prod-use","ns":"api"}' --json | json "d['window_id']")
rex -C "$app" do session.select session_id="$sid" window_id="$w"
rex api call -s "$sid" session.close_window "{\"window_id\":\"$w0\"}" >/dev/null
b=$(rex api call -s "$sid" session.list_blocks '{}' | json "[x['block_id'] for x in d['blocks'] if x['window_id']=='$w'][0]")
sleep 3

# klogs: the api workload's pods, merged, in a split; the cursor stays here
typ "$b" "klogs"; sleep 0.5; typ "$b" "$CR"; sleep 2.5
typ "$b" "deploy api"; sleep 1.5; typ "$b" "$CR"
sleep 5

# kroll: restart it, the tab header follows the rollout
typ "$b" "clear; kroll"; sleep 0.5; typ "$b" "$CR"; sleep 3
typ "$b" "use api/api"; sleep 1.5; typ "$b" "$CR"; sleep 2
typ "$b" "y"; sleep 0.6; typ "$b" "$CR"
sleep 14
echo "$sid"
