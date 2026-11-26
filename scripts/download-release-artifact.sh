#!/usr/bin/env bash
set -euo pipefail

: "${GITHUB_REPOSITORY:?Set GITHUB_REPOSITORY to the Tack repository.}"
: "${TACK_BUILD_RUN:?Set TACK_BUILD_RUN to the successful Tack workflow run.}"
: "${TACK_BUILD_ATTEMPT:?Set TACK_BUILD_ATTEMPT to the workflow attempt.}"
: "${RELEASE_ARCHIVE:?Set RELEASE_ARCHIVE to the artifact download path.}"
: "${GITHUB_OUTPUT:?Set GITHUB_OUTPUT to the GitHub Actions step output file.}"


if [[ ! "$TACK_BUILD_RUN" =~ ^[0-9]+$ ]]; then
    printf 'The Tack build run ID must be an integer.\n' >&2
    exit 1
fi
if [[ ! "$TACK_BUILD_ATTEMPT" =~ ^[0-9]+$ ]]; then
    printf 'The Tack build attempt must be an integer.\n' >&2
    exit 1
fi

artifact_metadata=$(gh api \
    "repos/${GITHUB_REPOSITORY}/actions/runs/${TACK_BUILD_RUN}/artifacts?name=tack-release-${TACK_BUILD_ATTEMPT}" \
    --jq '.artifacts | if length == 1 and .[0].expired == false then .[0] | [.id, .digest] | @tsv else error("Expected one unexpired release artifact for this build attempt") end')
IFS=$'\t' read -r artifact_id expected_digest <<< "$artifact_metadata"
if [[ ! "$artifact_id" =~ ^[0-9]+$ ]]; then
    printf 'GitHub did not identify the release artifact.\n' >&2
    exit 1
fi
if [[ ! "$expected_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
    printf 'GitHub did not provide the release artifact digest.\n' >&2
    exit 1
fi

gh api "repos/${GITHUB_REPOSITORY}/actions/artifacts/${artifact_id}/zip" > "$RELEASE_ARCHIVE"
actual_checksum=$(sha256sum "$RELEASE_ARCHIVE")
if [[ "sha256:${actual_checksum%% *}" != "$expected_digest" ]]; then
    printf 'The downloaded Tack release artifact failed SHA-256 verification.\n' >&2
    exit 1
fi

printf 'artifact-id=%s\n' "$artifact_id" >> "$GITHUB_OUTPUT"
