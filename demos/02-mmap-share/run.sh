#!/usr/bin/env bash
# Host-side runner: docker build + docker run
# Usage:  ./run.sh [N]
#   N = number of workload JVMs to run in parallel (default 4)
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
N="${1:-4}"
IMAGE="classcache-mmap-share"

cd "$HERE"

echo "==> docker build"
docker build -t "$IMAGE" . >&2

echo ""
echo "==> docker run (N=$N)"
docker run --rm -e N="$N" "$IMAGE"
