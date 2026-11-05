#!/bin/sh
# Creates a throwaway GitOps repo for the fwatch/fdiff demos: its remote is
# https://github.com/acme/fleet.git (what the demo clusters' GitRepository
# tracks) and its paths match the demo Kustomizations. Nothing is pushed.
#
#   demo/make-fleet.sh [DIR]   default: $TMPDIR/acme-fleet
set -eu
dir=${1:-${TMPDIR:-/tmp}/acme-fleet}
rm -rf "$dir"
mkdir -p "$dir/clusters/use" "$dir/clusters/euw" "$dir/clusters/staging" "$dir/apps/use/api" \
  "$dir/apps/euw" "$dir/apps/staging" "$dir/infrastructure"
cd "$dir"
git init -q -b main
git remote add origin https://github.com/acme/fleet.git
cat > apps/use/api/deployment.yaml <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: api
spec:
  replicas: 3
  template:
    spec:
      containers:
        - name: app
          image: ghcr.io/acme/api:1.18.2
          resources:
            limits:
              memory: 512Mi
EOF
for d in clusters/use clusters/euw clusters/staging apps/euw apps/staging infrastructure; do
  printf 'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n' > "$d/kustomization.yaml"
done
git add -A
git -c user.name=demo -c user.email=demo@example.com commit -qm "chore: initial fleet"
sed -i '' 's/api:1.18.2/api:1.19.0/; s/512Mi/768Mi/' apps/use/api/deployment.yaml
git -c user.name=demo -c user.email=demo@example.com commit -qam "feat(api): roll out api 1.19.0 with more memory"
echo "$dir"
