#!/bin/sh
# Drives a ~40s tour of rex8s in the real Rex app (demo mode), for a screen
# recording with captions: Cmd+Shift+J picker → a production pane → kfail →
# Cmd+Shift+M side by side → kall. Writes "step epoch" lines to $1 (default
# $TMPDIR/rex8s-tour.log) so captions can be lined up with the video.
#
#   rex8s demo on "$PWD/demo" && demo/tour-scene.sh && rex8s demo off
set -eu
log=${1:-${TMPDIR:-/tmp}/rex8s-tour.log}
: > "$log"
rex8s=${REX8S:-$HOME/.config/rex/rex8s/bin/rex8s}
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
mark() { python3 -c "import time; print('$1', time.time())" >> "$log"; }
blocks() { rex api call -s "$sid" session.list_blocks '{}'; }
first_in() { blocks | json "[b['block_id'] for b in d['blocks'] if b['window_id']=='$1'][0]"; }
typ() { rex send -s "$sid" -b "$1" "$2"; }
DOWN=$(printf '\033[B'); ESC=$(printf '\033'); CR=$(printf '\r')

app=$(rex do -e 'return rex.call("client.list")' --json | json "next(c['client_id'] for c in d['clients'] if (c.get('info') or {}).get('kind')=='app')")
out=$(rex new rex8s-demo --json)
sid=$(printf '%s' "$out" | json "d['session_id']")
w0=$(printf '%s' "$out" | json "d['initial_windows'][0]['window_id']")
show() { rex -C "$app" do session.select session_id="$sid" window_id="$1"; }

# 1. Cmd+Shift+J: pick a cluster, then a namespace
mark pick
w1=$(rex -s "$sid" do k8s_pick --json | json "d['window_id']")
show "$w1"
rex api call -s "$sid" session.close_window "{\"window_id\":\"$w0\"}" >/dev/null
b1=$(first_in "$w1")
sleep 3
typ "$b1" "use"; sleep 1.5; typ "$b1" "$CR"
sleep 2; typ "$b1" "api"; sleep 1.2; typ "$b1" "$CR"
sleep 3

# 2. kfail in that pane
mark kfail
typ "$b1" "kfail"; sleep 0.6; typ "$b1" "$CR"
sleep 6; typ "$b1" "$ESC"; sleep 1.5

# 3. Cmd+Shift+M: mark three clusters (euw, staging, use; sa is logged out)
mark multi
w2=$(rex -s "$sid" do k8s_multi_pick --json | json "d['window_id']")
show "$w2"
b2=$(first_in "$w2")
sleep 2.5
CTRLA=$(printf '\001'); BS=$(printf '\177')
typ "$b2" "u"; sleep 1; typ "$b2" "$CTRLA"; sleep 1          # euw and use, marked
typ "$b2" "$BS"; sleep 0.4; typ "$b2" "stag"; sleep 1; typ "$b2" "$CTRLA"; sleep 1   # and staging
typ "$b2" "$BS$BS$BS$BS"; sleep 1.2
typ "$b2" "$CR"
sleep 4

# 4. kall in the side-by-side tab
multi=$(blocks | json "[b['block_id'] for b in d['blocks'] if b['window_id'] not in ('$w0','$w1','$w2') and b['label'].startswith(('PROD ','k8s '))][0]")
mark kall
REX_BLOCK=$multi REX_SESSION=$sid "$rex8s" kall kubectl get deploy >/dev/null 2>&1
sleep 5
REX_BLOCK=$multi REX_SESSION=$sid "$rex8s" kall kubectl get pods -l app=worker >/dev/null 2>&1
sleep 6
mark end
echo "$sid"
