#!/usr/bin/env bash
# One-shot quickstart: kind cluster → cert-manager → cluster-classcache images
# → operator → quickstart ClassCache. Idempotent (re-runnable).
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
cd "$REPO"

KIND_NAME="${KIND_NAME:-cc-quickstart}"
BUILDER="${BUILDER:-docker}"

step() { echo ""; echo "═══════════════════════════════════════════════════════"; echo "  $*"; echo "═══════════════════════════════════════════════════════"; }

step "1/6  kind cluster ($KIND_NAME)"
if ! kind get clusters | grep -qx "$KIND_NAME"; then
    kind create cluster --name "$KIND_NAME" --config demos/06-k8s-end-to-end/kind-config.yaml
else
    echo "  cluster already exists — skipping"
fi
kubectl config use-context "kind-$KIND_NAME"

step "2/6  cert-manager"
if ! kubectl get ns cert-manager >/dev/null 2>&1; then
    kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.15.3/cert-manager.yaml
fi
kubectl -n cert-manager wait --for=condition=Available deploy --all --timeout=180s

step "3/6  Build images (operator + universal primer + scouter agent + demo app)"
BUILDER="$BUILDER" IMAGE=classcache-operator:v0.9.1           "$REPO/modules/operator/build.sh"
BUILDER="$BUILDER" IMAGE=classcache-primer:v0.9-universal     "$REPO/modules/primer/build-universal.sh"
BUILDER="$BUILDER" TAG=v0.9                                   "$REPO/modules/agent-catalog/build.sh"
if ! "$BUILDER" image inspect classcache-springboot-scale:latest >/dev/null 2>&1; then
    echo "  building demo app classcache-springboot-scale:latest"
    "$BUILDER" build -t classcache-springboot-scale:latest "$REPO/demos/03-springboot-scale"
fi

step "4/6  Load images into kind"
kind load docker-image --name "$KIND_NAME" \
    classcache-operator:v0.9.1 \
    classcache-primer:v0.9-universal \
    classcache-agent-scouter:v0.9 \
    classcache-springboot-scale:latest

step "5/6  Install operator + CRD + profile catalog"
kubectl apply -f deploy/manifests/00-classcache-crd.yaml
kubectl apply -f deploy/manifests/01-namespace-rbac.yaml
kubectl apply -f deploy/manifests/05-profile-catalog.yaml
kubectl apply -f deploy/manifests/04-cert-manager.yaml

# wait for cert secret then start operator (avoids the CrashLoopBackOff dance)
for i in $(seq 1 60); do
    kubectl -n classcache-system get secret classcache-webhook-tls >/dev/null 2>&1 && break
    sleep 1
done

sed 's|image: classcache-operator:latest|image: classcache-operator:v0.9.1|' \
    deploy/manifests/02-operator-deployment.yaml | kubectl apply -f -
kubectl apply -f deploy/manifests/03-mutatingwebhook.yaml
kubectl -n classcache-system rollout status deploy/classcache-operator --timeout=120s

step "6/6  Apply the quickstart ClassCache"
kubectl create ns cc-demo --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -f examples/quickstart.yaml

echo ""
echo "Waiting for ClassCache to reach Ready ..."
for i in $(seq 1 180); do
    PHASE=$(kubectl -n cc-demo get cc quickstart -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    KEY=$(kubectl   -n cc-demo get cc quickstart -o jsonpath='{.status.archiveKey}' 2>/dev/null || echo "")
    printf "\r  t=%3ds  phase=%-18s  key=%s" "$i" "${PHASE:-(none)}" "${KEY:-(none)}"
    if [[ "$PHASE" == "Ready" ]]; then
        echo ""; break
    fi
    sleep 1
done

echo ""
echo "═══════════════════════════════════════════════════════"
echo "  Result"
echo "═══════════════════════════════════════════════════════"
kubectl -n cc-demo get cc quickstart -o wide
echo ""
kubectl -n cc-demo get pod
echo ""
echo "Next: change examples/quickstart.yaml spec.app.image to your own image,"
echo "      change spec.agent.image to the catalog agent you want,"
echo "      then 'kubectl apply -f' your version."
echo ""
echo "Teardown: kind delete cluster --name $KIND_NAME"
