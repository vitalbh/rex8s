#!/bin/sh
# The palette scenes (Rex's native prompts), for screen recordings in the
# real Rex app with demo data. The person recording answers the palette;
# this script sets the stage and plays the "agent" in the guard scene.
#
#   rex8s demo on "$PWD/demo"
#   demo/palette-scene.sh stage     # session: euw cluster shell | "claude" pane; demo forwards
#   demo/palette-scene.sh guard     # the agent's kubectl apply on production → palette + diff
#   demo/palette-scene.sh clean     # stop the demo forwards, close the session
#   rex8s demo off
#
# Needs Remote Control on (Rex › Settings › Rex Server) to show the session.
set -eu
rex8s=${REX8S:-$HOME/.config/rex/rex8s/bin/rex8s}
guard=$(dirname "$rex8s")/rex8s-guard
state=${TMPDIR:-/tmp}/rex8s-palette-scene
json() { python3 -c "import json,sys; print($1)"; }

# demo mode on everywhere (Rex's actions run rex8s too); clean relies on it
# to stop only the demo's port-forwards
[ -s "${XDG_CONFIG_HOME:-$HOME/.config}/rex/rex8s/demo" ] ||
	{ echo "demo mode is off: rex8s demo on \"\$PWD/demo\"" >&2; exit 1; }

app() { rex do -e 'return rex.call("client.list")' --json | json "next(c['client_id'] for c in json.load(sys.stdin)['clients'] if (c.get('info') or {}).get('kind')=='app')"; }
slow() { s=$2; while [ -n "$s" ]; do c=${s%"${s#?}"}; s=${s#?}; rex send -s "$sid" -b "$1" "$c"; sleep 0.04; done; }

case "${1:-}" in
stage)
	# demo port-forwards for "Running Port-forwards" (lazy: no kubectl until used)
	"$rex8s" _pf acme-prod-euw api api 18080:8080 http >/dev/null 2>&1 &
	"$rex8s" _pf acme-prod-use api redis 16379:6379 redis >/dev/null 2>&1 &

	out=$(rex new rex8s-palette --json)
	sid=$(printf '%s' "$out" | json "json.load(sys.stdin)['session_id']")
	w0=$(printf '%s' "$out" | json "json.load(sys.stdin)['initial_windows'][0]['window_id']")
	win=$(rex -s "$sid" do k8s_tab --args '{"ctx":"acme-prod-euw","ns":"api"}' --json | json "json.load(sys.stdin)['window_id']")
	rex api call -s "$sid" session.close_window "{\"window_id\":\"$w0\"}" >/dev/null
	shell=$(rex api call -s "$sid" session.list_blocks '{}' | json "[b['block_id'] for b in json.load(sys.stdin)['blocks'] if b['window_id']=='$win'][0]")
	# the "agent" pane: a plain shell where kubectl goes through the guard
	agent=$(rex split -s "$sid" -b "$shell" --json --label claude --split right --ratio 45 --focus=false | json "json.load(sys.stdin)['block_ids'][0]")
	sleep 1.5
	rex send -s "$sid" -b "$agent" "alias kubectl='$guard kubectl'; export CLAUDECODE=1; clear$(printf '\r')"
	sleep 1
	# the agent's command, typed but not run: press enter in that pane for scene 3
	rex send -s "$sid" -b "$agent" "kubectl --context acme-prod-euw apply -f deploy/api.yaml"
	rex -C "$(app)" do session.select session_id="$sid" window_id="$win"
	printf 'sid=%s\nshell=%s\nagent=%s\n' "$sid" "$shell" "$agent" >"$state"
	cat <<'EOF'
Stage ready (session rex8s-palette). Scenes — open the palette (⇧⌘P), type, answer:
  1. Kubernetes: Cluster Health…        → euw → Pods → search-… → Logs
  2. Kubernetes: Favorite Port-forwards… → grafana (opens the browser; orders-db copies its URL)
     Kubernetes: Running Port-forwards   → api → Copy / Stop
     Kubernetes: Port-forward a Service… → euw → api → 8080
  3. click the "claude" pane, enter      → Allow once (the diff opens next to it)
     (or demo/palette-scene.sh guard types and runs it there)
  4. Kubernetes: Flux Resources…         → euw → payments-api → Reconcile now
  5. (focus the cluster shell) Kubernetes: Switch This Pane's Namespace… → payments
EOF
	;;
guard)
	. "$state"
	rex send -s "$sid" -b "$agent" "clear$(printf '\r')"
	sleep 1
	slow "$agent" "kubectl --context acme-prod-euw apply -f deploy/api.yaml"
	sleep 0.6
	rex send -s "$sid" -b "$agent" "$(printf '\r')"
	;;
clean)
	[ -f "$state" ] && . "$state" && rex kill "$sid" >/dev/null 2>&1 || true
	for pid in $("$rex8s" list pf | json "' '.join(str(e['pid']) for e in json.load(sys.stdin))"); do
		"$rex8s" kpf --stop "$pid" >/dev/null 2>&1 || true
	done
	rm -f "$state"
	;;
*)
	sed -n '2,12p' "$0"
	exit 2
	;;
esac
