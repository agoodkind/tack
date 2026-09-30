#!/usr/bin/env bash
set -euo pipefail

BASE="${REGISTRY}/${IMAGE_OWNER}/${IMAGE_NAME}"
TAGS=("${BASE}:${COMMIT_SHORT}" "${BASE}:${GITHUB_SHA}")
if [[ "$GITHUB_REF_TYPE" == "tag" ]]; then
    TAGS+=("${BASE}:${GITHUB_REF_NAME}" "${BASE}:latest")
else
    BRANCH_TAG="${BRANCH_NAME//\//-}"
    TAGS+=("${BASE}:${BRANCH_TAG}")
    if [[ "$BRANCH_NAME" == "main" ]]; then
        TAGS+=("${BASE}:latest")
    fi
fi
printf '%s\n' "${TAGS[@]}" > "$IMAGE_TAG_FILE"
