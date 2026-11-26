#!/usr/bin/env bash
set -euo pipefail

: "${GH_TOKEN:?Set GH_TOKEN to the Configs App dispatch token.}"
: "${TACK_BUILD_RUN:?Set TACK_BUILD_RUN to the successful Tack workflow run.}"
: "${TACK_BUILD_ATTEMPT:?Set TACK_BUILD_ATTEMPT to the workflow attempt.}"
: "${TACK_ARTIFACT_ID:?Set TACK_ARTIFACT_ID to the verified artifact ID.}"
: "${RELEASE_ARCHIVE:?Set RELEASE_ARCHIVE to the verified artifact ZIP.}"

MAX_ARCHIVE_BYTES=32768
ARCHIVE_SIZE=$(wc -c < "$RELEASE_ARCHIVE")
if (( ARCHIVE_SIZE < 1 || ARCHIVE_SIZE > MAX_ARCHIVE_BYTES )); then
    printf 'The Tack release archive must contain between 1 and %d bytes.\n' "$MAX_ARCHIVE_BYTES" >&2
    exit 1
fi

SCRIPT_DIRECTORY=$(dirname -- "${BASH_SOURCE[0]}")
ENCODED_ARCHIVE=$(openssl base64 -A -in "$RELEASE_ARCHIVE")
PAYLOAD_FILE=$(mktemp)
trap 'rm -f "$PAYLOAD_FILE"' EXIT

jq --null-input \
    --arg archive "$ENCODED_ARCHIVE" \
    --argjson run_id "$TACK_BUILD_RUN" \
    --argjson attempt "$TACK_BUILD_ATTEMPT" \
    --argjson artifact_id "$TACK_ARTIFACT_ID" \
    --from-file "$SCRIPT_DIRECTORY/release-dispatch.jq" \
    > "$PAYLOAD_FILE"
gh api --method POST repos/agoodkind/configs/dispatches --input "$PAYLOAD_FILE"
