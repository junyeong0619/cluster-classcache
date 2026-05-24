#!/usr/bin/env bash
# Build the classcache operator image (distroless/static, ~15 MB).
# BUILDER: docker (default) | podman | nerdctl
set -euo pipefail

BUILDER="${BUILDER:-docker}"
IMAGE="${IMAGE:-classcache-operator:latest}"

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"

if ! command -v "$BUILDER" >/dev/null 2>&1; then
    echo "error: BUILDER=$BUILDER not found in PATH" >&2
    exit 1
fi

echo "==> Building $IMAGE using $BUILDER"
"$BUILDER" build -t "$IMAGE" -f "$HERE/Dockerfile" "$REPO"

echo "==> Built $IMAGE"
"$BUILDER" image inspect "$IMAGE" --format '   size: {{.Size}} bytes ({{div .Size 1048576}} MiB)' 2>/dev/null || true
