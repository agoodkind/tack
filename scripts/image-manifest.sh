#!/usr/bin/env bash
set -euo pipefail

TAGS=()
while IFS= read -r TAG; do
    if [[ -n "$TAG" ]]; then
        TAGS+=(--tag "$TAG")
    fi
done < "$IMAGE_TAG_FILE"
if [[ ${#TAGS[@]} -eq 0 ]]; then
    printf 'Image manifest requires at least one tag\n' >&2
    exit 1
fi

SOURCES=()
for IMAGE_ARCH in amd64 arm64; do
    IMAGE_DIGEST=$(cat "${DIGEST_DIRECTORY}/${IMAGE_ARCH}.digest")
    if [[ ! "$IMAGE_DIGEST" =~ ^sha256:[a-f0-9]{64}$ ]]; then
        printf 'Invalid %s image digest\n' "$IMAGE_ARCH" >&2
        exit 1
    fi
    SOURCES+=("${REGISTRY}/${IMAGE_OWNER}/${IMAGE_NAME}@${IMAGE_DIGEST}")
done
docker buildx imagetools create "${TAGS[@]}" "${SOURCES[@]}"
