#!/usr/bin/env bash
set -euo pipefail

COMMIT_SHA=$(git rev-parse HEAD)
SHORT_SHA=$(git rev-parse --short HEAD)
BUILD_TIME=$(date -u +%FT%TZ)
if TAG=$(git describe --tags --exact-match HEAD); then
    printf 'Build metadata uses tag %s\n' "$TAG"
else
    printf 'HEAD has no exact tag; build metadata uses branch %s\n' "$BRANCH_NAME"
    TAG="$BRANCH_NAME"
fi
{
    printf 'commit=%s\n' "$COMMIT_SHA"
    printf 'short=%s\n' "$SHORT_SHA"
    printf 'build_time=%s\n' "$BUILD_TIME"
    printf 'tag=%s\n' "$TAG"
} >> "$GITHUB_OUTPUT"
