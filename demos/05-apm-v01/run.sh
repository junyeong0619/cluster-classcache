#!/usr/bin/env bash
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
IMAGE="classcache-apm-v0.1"
cd "$HERE"
echo "==> docker build"
docker build -t "$IMAGE" . >&2
echo ""
echo "==> docker run (verify)"
docker run --rm "$IMAGE"
