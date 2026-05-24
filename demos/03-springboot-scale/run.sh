#!/usr/bin/env bash
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
N="${1:-4}"
IMAGE="classcache-springboot-scale"
cd "$HERE"
echo "==> docker build (Spring Boot + agent + archive, takes several minutes)"
docker build -t "$IMAGE" . >&2
echo ""
echo "==> docker run (N=$N)"
docker run --rm -e N="$N" "$IMAGE"
