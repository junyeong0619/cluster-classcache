#!/usr/bin/env bash
# Build the v0.9 universal primer image — no app, no agent, no profile baked in.
set -euo pipefail

BUILDER="${BUILDER:-docker}"
IMAGE="${IMAGE:-classcache-primer:v0.9-universal}"

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"

if ! command -v "$BUILDER" >/dev/null 2>&1; then
    echo "error: BUILDER=$BUILDER not found in PATH" >&2
    exit 1
fi

echo "==> Building $IMAGE using $BUILDER (universal — no app/agent baked in)"
"$BUILDER" build -t "$IMAGE" -f "$HERE/Dockerfile.universal" "$REPO"

echo "==> Built $IMAGE"
"$BUILDER" image inspect "$IMAGE" --format '   size: {{.Size}} bytes ({{div .Size 1048576}} MiB)' 2>/dev/null || true
