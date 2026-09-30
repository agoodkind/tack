#!/usr/bin/env bash
set -euo pipefail

case "$IMAGE_ARCH" in
    amd64|arm64) ;;
    *) printf 'Unsupported image architecture: %s\n' "$IMAGE_ARCH" >&2; exit 1 ;;
esac
if [[ ! "$IMAGE_DIGEST" =~ ^sha256:[a-f0-9]{64}$ ]]; then
    printf 'Build output is not a SHA256 image digest\n' >&2
    exit 1
fi
mkdir -p "$DIGEST_DIRECTORY"
printf '%s\n' "$IMAGE_DIGEST" > "${DIGEST_DIRECTORY}/${IMAGE_ARCH}.digest"
