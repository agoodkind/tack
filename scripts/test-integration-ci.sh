#!/usr/bin/env bash

set -euo pipefail

cd "$(dirname "$0")/.."

cleanup() {
    local command_status="$1"
    local cleanup_status=0

    trap - EXIT INT TERM
    if ./scripts/test-fdb-down.sh; then
        cleanup_status=0
    else
        cleanup_status=$?
        echo "integration cleanup failed with status ${cleanup_status}" >&2
    fi

    if [[ "$command_status" -ne 0 ]]; then
        exit "$command_status"
    fi
    exit "$cleanup_status"
}

handle_interrupt() {
    exit 130
}

# GitHub signals the active step when canceling a run. One process owns setup,
# tests, and teardown so this trap removes the run's Compose project.
trap 'cleanup "$?"' EXIT
trap handle_interrupt INT TERM

./scripts/test-fdb-up.sh
./scripts/test-integration.sh
