#!/usr/bin/env bash
# Build the catalog of APM agent images. Each subdirectory under
# modules/agent-catalog/<name>/ produces classcache-agent-<name>:<tag>.
set -euo pipefail

BUILDER="${BUILDER:-docker}"
TAG="${TAG:-v0.9}"

HERE="$(cd "$(dirname "$0")" && pwd)"

for d in "$HERE"/*/; do
    name=$(basename "$d")
    image="classcache-agent-${name}:${TAG}"
    echo "==> Building $image"
    "$BUILDER" build -t "$image" "$d"
done

echo "==> Built:"
"$BUILDER" image ls | grep '^classcache-agent-' || true
