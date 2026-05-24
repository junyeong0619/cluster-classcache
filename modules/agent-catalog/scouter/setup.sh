#!/usr/bin/env bash
# One-shot installer for the Scouter agent catalog image.
#
# What it does:
#   1. Downloads the official Scouter release tarball from GitHub.
#   2. Extracts scouter.agent.jar + scouter.conf to this directory.
#   3. Builds classcache-agent-scouter:<TAG>.
#   4. (optional) Loads the image into a kind cluster — set KIND_NAME.
#
# Why this script exists: unlike OpenTelemetry, Datadog, New Relic, and
# Elastic — which all ship official Docker images for their agent jars —
# Scouter only releases tarballs. So we need one small wrapper.
set -euo pipefail

VER="${VER:-2.20.0}"
TAG="${TAG:-v0.9}"
BUILDER="${BUILDER:-docker}"
KIND_NAME="${KIND_NAME:-}"
IMAGE="${IMAGE:-classcache-agent-scouter:${TAG}}"

HERE="$(cd "$(dirname "$0")" && pwd)"
cd "$HERE"

URL="https://github.com/scouter-project/scouter/releases/download/v${VER}/scouter-min-${VER}.tar.gz"
TARBALL="scouter-min-${VER}.tar.gz"

if [[ ! -f "scouter.agent.jar" ]]; then
    if [[ ! -f "$TARBALL" ]]; then
        echo "==> Downloading Scouter $VER from GitHub"
        curl -fsSL -o "$TARBALL" "$URL"
    fi
    echo "==> Extracting scouter.agent.jar + scouter.conf"
    tar -xzf "$TARBALL" --strip-components=2 \
        "scouter/agent.java/scouter.agent.jar" \
        "scouter/agent.java/conf/scouter.conf" 2>/dev/null \
      || tar -xzf "$TARBALL" -C /tmp \
      && cp "/tmp/scouter/agent.java/scouter.agent.jar" . \
      && cp "/tmp/scouter/agent.java/conf/scouter.conf" .
    rm -f "$TARBALL"
fi

if [[ ! -f scouter.agent.jar ]] || [[ ! -f scouter.conf ]]; then
    echo "ERROR: scouter.agent.jar or scouter.conf missing after extract." >&2
    echo "       Drop them manually under $HERE/ and re-run." >&2
    exit 1
fi

echo "==> Building $IMAGE using $BUILDER"
"$BUILDER" build -t "$IMAGE" .

if [[ -n "$KIND_NAME" ]]; then
    echo "==> Loading $IMAGE into kind cluster '$KIND_NAME'"
    kind load docker-image "$IMAGE" --name "$KIND_NAME"
fi

echo ""
echo "==> Done."
echo "   Image: $IMAGE"
"$BUILDER" image inspect "$IMAGE" --format '   Size:  {{.Size}} bytes ({{div .Size 1048576}} MiB)' 2>/dev/null || true
echo ""
echo "Use it in a ClassCache:"
echo "   spec:"
echo "     agent:"
echo "       image:      $IMAGE"
echo "       jarPath:    /agent.jar"
echo "       configPath: /agent.conf"
