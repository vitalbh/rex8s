#!/bin/sh
# Records the README GIFs with VHS (brew install vhs) against the demo data.
# Demo mode is enabled only for this process (REX8S_DEMO), not globally.
#
#   demo/record.sh            all tapes
#   demo/record.sh kflux      just demo/tapes/kflux.tape
#   REX8S_DEMO_BIN=dir demo/record.sh kview   with the rex8s in dir
set -eu
cd "$(dirname "$0")/.."
root=$(pwd)
command -v vhs >/dev/null || { echo "vhs not found: brew install vhs" >&2; exit 1; }

export REX8S_DEMO="$root/demo"
# kflux, kwhy, … wrappers: the installed ones, or REX8S_DEMO_BIN (a build not
# installed yet: a directory with rex8s, rex8s-guard and the wrappers)
export PATH="${REX8S_DEMO_BIN:-$HOME/.config/rex/rex8s/bin}:$PATH"
unset REX_BLOCK REX_SESSION KUBECONFIG            # never inside a real Rex pane / cluster
export GIT_PAGER=cat PAGER=cat
export DEMO_FLEET="$("$root/demo/make-fleet.sh")"
state="${TMPDIR:-/tmp}/rex8s-demo"
mkdir -p "$state"
# a pane-style kubeconfig pointing at the euw cluster (for kflux)
printf 'apiVersion: v1\nkind: Config\ncurrent-context: acme-prod-euw\ncontexts:\n- name: acme-prod-euw\n  context: {cluster: euw, user: demo, namespace: payments}\n' > "$state/euw.yaml"
export DEMO_EUW="$state/euw.yaml:$root/demo/kubeconfig"

tapes=${1:-}
for t in demo/tapes/${tapes:-*}.tape; do
  rm -f "$state/fwatch-t0" "$state/crash-t0" "$state"/restart-*
  echo "recording $t"
  vhs "$t"
done
