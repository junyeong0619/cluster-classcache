#!/usr/bin/env bash
# Build the distroless primer image.
# BUILDER lets you switch container runtime: docker (default), podman, nerdctl.
set -euo pipefail

BUILDER="${BUILDER:-docker}"
IMAGE="${IMAGE:-classcache-primer:latest}"
PROFILE="${PROFILE:-scouter}"   # which profile YAML to bake as default

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"

if ! command -v "$BUILDER" >/dev/null 2>&1; then
    echo "error: BUILDER=$BUILDER not found in PATH" >&2
    exit 1
fi

PROFILE_PATH="modules/agent-profiles/profiles/${PROFILE}.yaml"
if [[ ! -f "$REPO/$PROFILE_PATH" ]]; then
    echo "error: profile not found: $PROFILE_PATH" >&2
    exit 1
fi

echo "==> Building $IMAGE using $BUILDER (profile=$PROFILE)"
"$BUILDER" build \
    -t "$IMAGE" \
    -f "$HERE/Dockerfile" \
    --build-arg "PROFILE_PATH=$PROFILE_PATH" \
    "$REPO"

echo "==> Built $IMAGE"
"$BUILDER" image inspect "$IMAGE" --format '   size: {{.Size}} bytes ({{div .Size 1048576}} MiB)' 2>/dev/null || true
