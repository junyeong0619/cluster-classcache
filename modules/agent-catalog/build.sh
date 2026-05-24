#!/usr/bin/env bash
# Build every catalog agent image. Catalog images exist only for vendors
# that don't ship an official Docker image (today: Scouter).
#
# For Scouter, this just delegates to scouter/setup.sh, which also handles
# downloading the upstream tarball when scouter.agent.jar isn't present yet.
set -euo pipefail

BUILDER="${BUILDER:-docker}"
TAG="${TAG:-v0.9}"
KIND_NAME="${KIND_NAME:-}"

HERE="$(cd "$(dirname "$0")" && pwd)"

for d in "$HERE"/*/; do
    name=$(basename "$d")
    if [[ -x "$d/setup.sh" ]]; then
        echo "==> $name: running setup.sh"
        BUILDER="$BUILDER" TAG="$TAG" KIND_NAME="$KIND_NAME" "$d/setup.sh"
    else
        image="classcache-agent-${name}:${TAG}"
        echo "==> $name: building $image"
        "$BUILDER" build -t "$image" "$d"
        if [[ -n "$KIND_NAME" ]]; then
            kind load docker-image "$image" --name "$KIND_NAME"
        fi
    fi
done

echo ""
echo "==> Built:"
"$BUILDER" image ls | grep '^classcache-agent-' || true
