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
# RELEASE_IMAGE_DIRECTORY stores temporary digest and release note files.
#
# To annotate an existing release for a specific commit, run:
# env GITHUB_REPOSITORY=agoodkind/tack \
#     GITHUB_SHA=419c72366974c022100a917d79b7eb6b06eb90fd \
#     RELEASE_TAG=202610180456-2-419c723 \
#     RELEASE_IMAGE_DIRECTORY=/tmp/tack-release-images-419c723 \
#     bash scripts/release-images.sh
#
# To verify the release attestation separately, run:
# gh release verify 202610180456-2-419c723 --repo agoodkind/tack

set -euo pipefail

: "${GITHUB_REPOSITORY:?Set GITHUB_REPOSITORY to the release repository.}"
: "${GITHUB_SHA:?Set GITHUB_SHA to the released commit.}"
: "${RELEASE_TAG:?Set RELEASE_TAG to the published release tag.}"
: "${RELEASE_IMAGE_DIRECTORY:?Set RELEASE_IMAGE_DIRECTORY to a temporary directory.}"

resolve_image_reference() {
    local image_name="$1"
    local image_repository="ghcr.io/${GITHUB_REPOSITORY%/*}/${image_name}"
    local digest_file="${RELEASE_IMAGE_DIRECTORY}/${image_name}.digest"
    local image_digest

    docker buildx imagetools inspect "${image_repository}:${GITHUB_SHA}" \
        --format '{{.Manifest.Digest}}' > "$digest_file"
    printf '\n' >> "$digest_file"
    IFS= read -r image_digest < "$digest_file"
    if [[ ! "$image_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
        printf 'The image %s did not return a SHA-256 digest.\n' "$image_repository" >&2
        return 1
    fi
    printf '%s@%s\n' "$image_repository" "$image_digest" \
        > "${RELEASE_IMAGE_DIRECTORY}/${image_name}.reference"
}

main() {
    local commit_file="${RELEASE_IMAGE_DIRECTORY}/release-commit"
    local notes_file="${RELEASE_IMAGE_DIRECTORY}/release-notes.md"
    local release_commit
    local server_reference
    local audit_reference
    local marker

    mkdir -p "$RELEASE_IMAGE_DIRECTORY"
    gh release view "$RELEASE_TAG" --repo "$GITHUB_REPOSITORY" \
        --json targetCommitish --jq .targetCommitish > "$commit_file"
    IFS= read -r release_commit < "$commit_file"
    if [[ "$release_commit" != "$GITHUB_SHA" ]]; then
        printf 'Release %s targets %s instead of %s.\n' \
            "$RELEASE_TAG" "$release_commit" "$GITHUB_SHA" >&2
        return 1
    fi

    resolve_image_reference tack-server
    resolve_image_reference tack-audit-consumer
    IFS= read -r server_reference < "${RELEASE_IMAGE_DIRECTORY}/tack-server.reference"
    IFS= read -r audit_reference < "${RELEASE_IMAGE_DIRECTORY}/tack-audit-consumer.reference"
    marker="<!-- tack-images:${GITHUB_SHA}:${server_reference}:${audit_reference} -->"

    gh release view "$RELEASE_TAG" --repo "$GITHUB_REPOSITORY" \
        --json body --jq .body > "$notes_file"
    if grep -Fqx -- "$marker" "$notes_file"; then
        printf 'Release %s already includes both container image references.\n' "$RELEASE_TAG"
    else
        printf '\n\n## Container images\n\n%s\n%s\n%s\n%s\n\n%s\n' \
            '| Image | Reference |' '| --- | --- |' \
            "| tack-server | ${server_reference} |" \
            "| tack-audit-consumer | ${audit_reference} |" \
            "$marker" >> "$notes_file"
        gh release edit "$RELEASE_TAG" --repo "$GITHUB_REPOSITORY" \
            --notes-file "$notes_file"
    fi

    gh release verify "$RELEASE_TAG" --repo "$GITHUB_REPOSITORY"
}

main
