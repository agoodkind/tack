#!/usr/bin/env bash
set -euo pipefail

RUNNER_ARCH=$(uname -m)
case "${IMAGE_ARCH}:${RUNNER_ARCH}" in
    amd64:x86_64|arm64:aarch64) printf 'Native image runner: %s\n' "$RUNNER_ARCH" ;;
    *) printf 'Image architecture %s does not match runner %s\n' "$IMAGE_ARCH" "$RUNNER_ARCH" >&2; exit 1 ;;
esac
