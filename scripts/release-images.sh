#!/usr/bin/env bash

# Add container image references to a published Tack release.
#
# The image workflow publishes tack-server and tack-audit-consumer before the
# provider workflow publishes its binaries. This script adds both image
# references by SHA-256 digest to the provider release notes.
#
# GitHub prevents changes to immutable release assets and tags, but permits
# release note edits. This script preserves existing notes, skips repeated
# annotations, and verifies the release attestation, a signed record of the
# provider assets. It rejects a release that targets a different commit.
#
# Use Docker with Buildx and authenticate Docker for registry reads. Authenticate
# the GitHub CLI with permission to read and update the repository's releases.
# The workflow supplies both image repository addresses.
#
# To annotate an existing release for a specific commit, run:
# env GITHUB_REPOSITORY=agoodkind/tack \
#     GITHUB_SHA=419c72366974c022100a917d79b7eb6b06eb90fd \
#     RELEASE_TAG=202610180456-2-419c723 \
#     TACK_SERVER_IMAGE=ghcr.io/agoodkind/tack-server \
#     TACK_AUDIT_CONSUMER_IMAGE=ghcr.io/agoodkind/tack-audit-consumer \
#     bash scripts/release-images.sh
#
# To verify the release attestation separately, run:
# gh release verify 202610180456-2-419c723 --repo agoodkind/tack

set -euo pipefail

: "${GITHUB_REPOSITORY:?Set GITHUB_REPOSITORY to the release repository.}"
: "${GITHUB_SHA:?Set GITHUB_SHA to the released commit.}"
: "${RELEASE_TAG:?Set RELEASE_TAG to the published release tag.}"
: "${TACK_SERVER_IMAGE:?Set TACK_SERVER_IMAGE to the server image repository.}"
: "${TACK_AUDIT_CONSUMER_IMAGE:?Set TACK_AUDIT_CONSUMER_IMAGE to the audit consumer image repository.}"

fetch_release_commit() {
    gh release view "$RELEASE_TAG" --repo "$GITHUB_REPOSITORY" \
        --json targetCommitish --jq .targetCommitish
}

check_release_commit() {
    local release_commit="$1"
    local expected_commit="$2"

    if [[ "$release_commit" != "$expected_commit" ]]; then
        printf 'Release %s targets %s instead of %s.\n' \
            "$RELEASE_TAG" "$release_commit" "$expected_commit" >&2
        return 1
    fi
}

fetch_image_digest() {
    local image_reference="$1"

    docker buildx imagetools inspect "$image_reference" --format '{{.Manifest.Digest}}'
}

resolve_image_reference() {
    local image_repository="$1"
    local image_digest

    if ! image_digest="$(fetch_image_digest "${image_repository}:${GITHUB_SHA}")"; then
        return 1
    fi
    if [[ ! "$image_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
        printf 'The image %s did not return a SHA-256 digest.\n' "$image_repository" >&2
        return 1
    fi
    printf '%s@%s\n' "$image_repository" "$image_digest"
}

fetch_release_notes() {
    gh release view "$RELEASE_TAG" --repo "$GITHUB_REPOSITORY" --json body --jq .body
}

notes_include_marker() {
    local notes="$1"
    local marker="$2"

    [[ "$notes" == *"$marker"* ]]
}

append_image_notes() {
    local notes="$1"
    local server_reference="$2"
    local audit_reference="$3"
    local marker="$4"

    printf '%s\n\n## Container images\n\n%s\n%s\n%s\n%s\n\n%s\n' \
        "$notes" '| Image | Reference |' '| --- | --- |' \
        "| tack-server | ${server_reference} |" \
        "| tack-audit-consumer | ${audit_reference} |" "$marker"
}

publish_release_notes() {
    local notes="$1"

    gh release edit "$RELEASE_TAG" --repo "$GITHUB_REPOSITORY" --notes "$notes"
}

verify_release() {
    gh release verify "$RELEASE_TAG" --repo "$GITHUB_REPOSITORY"
}

main() {
    local release_commit
    local server_reference
    local audit_reference
    local marker
    local notes

    release_commit="$(fetch_release_commit)"
    check_release_commit "$release_commit" "$GITHUB_SHA"
    server_reference="$(resolve_image_reference "$TACK_SERVER_IMAGE")"
    audit_reference="$(resolve_image_reference "$TACK_AUDIT_CONSUMER_IMAGE")"
    marker="<!-- tack-images:${GITHUB_SHA}:${server_reference}:${audit_reference} -->"
    notes="$(fetch_release_notes)"

    if notes_include_marker "$notes" "$marker"; then
        printf 'Release %s already includes both container image references.\n' "$RELEASE_TAG"
    else
        notes="$(append_image_notes "$notes" "$server_reference" "$audit_reference" "$marker")"
        publish_release_notes "$notes"
    fi
    verify_release
}

main
