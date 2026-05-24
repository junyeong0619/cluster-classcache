#!/usr/bin/env bash
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
cd "$HERE"
docker build -t classcache-otel-trial . >&2
docker run --rm classcache-otel-trial
