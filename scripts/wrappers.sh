#!/bin/sh
# Generates the wrappers in $1 (the directory rex8s panes put on PATH).
set -eu
dir=$1

for tool in kubectl helm flux; do
  cat > "$dir/$tool" <<EOF
#!/bin/sh
exec "\$(dirname "\$0")/rex8s-guard" $tool "\$@"
EOF
done

cat > "$dir/kns" <<'EOF'
#!/bin/sh
# kns [NS] - switch this pane's namespace (no argument: picker)
exec "$(dirname "$0")/rex8s" ns "$@"
EOF

cat > "$dir/kctx" <<'EOF'
#!/bin/sh
# kctx [CTX] - switch this pane's cluster (no argument: picker)
exec "$(dirname "$0")/rex8s" switch "$@"
EOF

for pair in "fwatch fwatch" "kflux flux" "klogs logs" "fdiff fdiff" "kpf kpf" "kauth kauth" "kexec kexec" "kwarn kwarn" "kwhy kwhy" "kfail kfail"; do
  set -- $pair
  printf '#!/bin/sh\nexec "$(dirname "$0")/rex8s" %s "$@"\n' "$2" > "$dir/$1"
done

chmod 0755 "$dir/kubectl" "$dir/helm" "$dir/flux" "$dir/kns" "$dir/kctx" "$dir/fwatch" "$dir/kflux" "$dir/klogs" "$dir/fdiff" "$dir/kpf" "$dir/kauth" "$dir/kexec" "$dir/kwarn" "$dir/kwhy" "$dir/kfail"
