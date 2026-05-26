#!/usr/bin/env bash
# k3d 3-node verification — true multi-node container path.
#
# Why k3d (not kind): kind nodes are co-located in one Docker container by
# design; k3d gives you N separate node containers, so peer-to-peer pulls
# actually traverse a Docker bridge network. Still not real cross-host
# (managed K8s would be), but it's the strongest signal you can get on a
# laptop.
#
# Usage:  ./run.sh
# Cleanup: k3d cluster delete cc-k3d
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
CLUSTER="${CLUSTER:-cc-k3d}"
BUILDER="${BUILDER:-docker}"

step() { echo ""; echo "==================================="; echo "  $*"; echo "==================================="; }

step "1) Create k3d cluster with 1 server + 3 agents"
if ! k3d cluster list | awk 'NR>1 {print $1}' | grep -qx "$CLUSTER"; then
    k3d cluster create "$CLUSTER" \
        --servers 1 --agents 3 \
        --k3s-arg '--disable=traefik@server:*' \
        --wait
else
    echo "  cluster '$CLUSTER' already exists"
    kubectl config use-context "k3d-$CLUSTER"
fi
kubectl config use-context "k3d-$CLUSTER"
kubectl get nodes -o wide

step "2) cert-manager"
if ! kubectl get ns cert-manager >/dev/null 2>&1; then
    kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.15.3/cert-manager.yaml
fi
kubectl -n cert-manager wait --for=condition=Available deploy --all --timeout=180s

step "3) Build images (if not already) + k3d import"
"$REPO/modules/operator/build.sh" 2>&1 | tail -3 || true
BUILDER="$BUILDER" IMAGE=classcache-operator:v0.9.1 "$REPO/modules/operator/build.sh"
BUILDER="$BUILDER" IMAGE=classcache-primer:v0.9-universal "$REPO/modules/primer/build-universal.sh"
BUILDER="$BUILDER" TAG=v0.9 "$REPO/modules/agent-catalog/build.sh"
"$BUILDER" image inspect classcache-springboot-scale:latest >/dev/null 2>&1 \
    || "$BUILDER" build -t classcache-springboot-scale:latest "$REPO/demos/03-springboot-scale"

# k3d import is the analog of `kind load docker-image`.
k3d image import -c "$CLUSTER" \
    classcache-operator:v0.9.1 \
    classcache-primer:v0.9-universal \
    classcache-agent-scouter:v0.9 \
    classcache-springboot-scale:latest

step "4) Install operator + profile catalog"
kubectl apply -f "$REPO/deploy/manifests/00-classcache-crd.yaml"
kubectl apply -f "$REPO/deploy/manifests/01-namespace-rbac.yaml"
kubectl apply -f "$REPO/deploy/manifests/05-profile-catalog.yaml"
kubectl apply -f "$REPO/deploy/manifests/04-cert-manager.yaml"
for i in $(seq 1 60); do
    kubectl -n classcache-system get secret classcache-webhook-tls >/dev/null 2>&1 && break
    sleep 1
done
sed 's|image: classcache-operator:latest|image: classcache-operator:v0.9.1|' \
    "$REPO/deploy/manifests/02-operator-deployment.yaml" | kubectl apply -f -
kubectl apply -f "$REPO/deploy/manifests/03-mutatingwebhook.yaml"
kubectl -n classcache-system rollout status deploy/classcache-operator --timeout=120s

step "5) Apply quickstart ClassCache"
kubectl create ns cc-demo --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f "$REPO/examples/quickstart.yaml"

echo ""
echo "Waiting for Ready..."
for i in $(seq 1 240); do
    PHASE=$(kubectl -n cc-demo get cc quickstart -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    KEY=$(kubectl   -n cc-demo get cc quickstart -o jsonpath='{.status.archiveKey}' 2>/dev/null || echo "")
    printf "\r  t=%3ds  phase=%-18s  key=%s" "$i" "${PHASE:-(none)}" "${KEY:-(none)}"
    [[ "$PHASE" == "Ready" ]] && { echo ""; break; }
    sleep 1
done

step "6) Result"
kubectl -n cc-demo get cc quickstart -o wide
echo ""
kubectl -n cc-demo get pod -o wide
echo ""
echo "Workload spread across nodes:"
kubectl -n cc-demo get pod -l app=quickstart \
    -o custom-columns=POD:.metadata.name,NODE:.spec.nodeName,IP:.status.podIP

step "7) Hint"
echo "Run live stats:"
echo "  kubectl -n cc-demo port-forward svc/cc-quickstart-valkey 6379:6379 &"
echo "  classcache stats"
echo ""
echo "Teardown: k3d cluster delete $CLUSTER"
