#!/usr/bin/env bash
# One-shot installer for the Pinpoint agent catalog image.
#
# Pinpoint releases the agent as a tarball; it's not a single jar — boot/,
# lib/, and plugin/ all have to land next to the bootstrap jar at runtime.
# So this catalog image carries the whole pinpoint-agent-<VER>/ tree.
#
# Why we wrap Pinpoint at all: there's no official Docker image for the agent
# (only for the collector and web UI), and the bootstrap jar references
# plugin classes via the agent directory layout, not a flat path.
set -euo pipefail

VER="${VER:-3.1.0}"
TAG="${TAG:-v0.10}"
BUILDER="${BUILDER:-docker}"
KIND_NAME="${KIND_NAME:-}"
IMAGE="${IMAGE:-classcache-agent-pinpoint:${TAG}}"

HERE="$(cd "$(dirname "$0")" && pwd)"
cd "$HERE"

URL="https://github.com/pinpoint-apm/pinpoint/releases/download/v${VER}/pinpoint-agent-${VER}.tar.gz"
TARBALL="pinpoint-agent-${VER}.tar.gz"

if [[ ! -d agent ]]; then
    if [[ ! -f "$TARBALL" ]]; then
        echo "==> Downloading Pinpoint agent $VER from GitHub"
        curl -fsSL -o "$TARBALL" "$URL"
    fi
    echo "==> Extracting agent tree"
    mkdir -p agent
    tar -xzf "$TARBALL" -C agent --strip-components=1

    # Pinpoint ships pinpoint-bootstrap-<VER>.jar — alias to a version-less
    # name so the catalog image doesn't bake in the version.
    BOOTSTRAP=$(ls agent/pinpoint-bootstrap-*.jar 2>/dev/null | head -1 || true)
    if [[ -z "$BOOTSTRAP" ]]; then
        echo "ERROR: pinpoint-bootstrap-*.jar not found after extract" >&2
        exit 1
    fi
    ln -sf "$(basename "$BOOTSTRAP")" agent/pinpoint-bootstrap.jar
    rm -f "$TARBALL"
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
echo "       image:    $IMAGE"
echo "       jarPath:  /agent     # NOTE: directory, not a single jar"
echo "                            # the operator copies the whole /agent tree"
echo "                            # and points -javaagent at agent/pinpoint-bootstrap.jar"
