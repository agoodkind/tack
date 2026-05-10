# Audit Parity Subcommand Implementation Report

Date: 2026-05-09
Branch: phase2-wave1-rebase
Scope: Phase 2 Wave 1 dual-write parity tool

## Files added

- `internal/ops/audit_parity.go` (198 lines): subcommand entrypoint
  `runAuditParity`, the env-driven `readParityWindow` and
  `readParityThreshold`, the in-memory `runParityScan` orchestrator, the
  `parityScanner` interface that decouples scan logic from pgx, and
  `writeParityResult` which renders pretty-printed JSON to stdout.
- `internal/ops/audit_parity_pgx.go` (174 lines): the production
  `pgxParityScanner` that wraps a `pgxpool.Pool`, the two SQL queries
  (`parityCountsSQL` for aggregated FILTER counts, `parityExamplesSQL`
  for up to N content-diff examples), the per-field diff collector
  `collectFieldDiffs`, and the `openParityPool` constructor.
- `internal/ops/audit_parity_test.go` (175 lines): scan-level tests
  covering zero-row windows, full-match, only-legacy drift, only-v2
  drift, content-diff with example pulls, and propagation of scanner
  errors. Uses a hand-rolled `fakeParityScanner` to avoid faking
  `pgx.Rows`.
- `internal/ops/audit_parity_window_test.go` (105 lines): env-parsing
  tests for `readParityWindow` (threshold parsing, default threshold,
  inverted window rejection, out-of-range threshold rejection) and
  unit tests for `collectFieldDiffs`.

## Files modified

- `internal/ops/command.go`: added `commandFamily` named string enum and
  the `audit` family branch in `RunCommand` and `printFamilyUsage`,
  plus a new `runAuditCommand` dispatcher that routes
  `./server ops audit parity`. Existing dispatch shape patterned off
  `runInspectCommand` etc. (see `internal/ops/command.go:65-81` in the
  pre-edit file). Switching the top-level switch to a named enum was
  required to avoid breaking the staticcheck-extra "named enum"
  baseline match (the baseline pinned the count of cases).
- `internal/ops/command_help.go`: added `printAuditUsage` and listed
  the new family in `printOpsUsage`. The help text spells out the
  three env vars (`TACK_PARITY_FROM`, `TACK_PARITY_TO`,
  `TACK_PARITY_THRESHOLD`).

## Files NOT modified (and why)

- `cmd/server/main.go`: the prompt asked for a one-line addition here,
  but the existing `case "ops":` branch already routes through
  `runOps -> ops.RunCommand`, which now handles the new `audit`
  family. No edit was needed; adding one would have been a no-op.
  Flagged for the caller in case the prompt was implying a different
  surface.
- `internal/audit/dual.go`, `internal/audit/clock.go`,
  `internal/audit/monitoring_test.go`: these are untracked WIP files
  in the worktree. `make fmt` (run as part of `make build`) auto-fixed
  the dual.go `time` import vs `monoStart()` mismatch and the trailing
  newline in monitoring_test.go. None of those files were edited by
  this slice.

## Subcommand reference

```
./server ops audit parity
```

Env vars:
- `TACK_PARITY_FROM`: inclusive lower bound, RFC3339 UTC. Required.
- `TACK_PARITY_TO`: exclusive upper bound, RFC3339 UTC. Required, must
  be strictly after `TACK_PARITY_FROM`.
- `TACK_PARITY_THRESHOLD`: matched-fraction floor in `[0, 1]`. Default
  `1.0`.

DSN resolution: prefers `AUDIT_READER_DSN`, falls back to
`AUDIT_WRITER_DSN`. The reader role has SELECT on both
`audit.events` and `audit.events_v2` per
`migrations/005_audit_events_v2_sibling.sql:48`.

Exit codes:
- `0` when total rows in window is zero, or matched fraction
  satisfies the threshold.
- non-zero when total rows in window is positive and the matched
  fraction is strictly below threshold.

## SQL design

Two queries, both server-side, both bounded by the same window:

1. `parityCountsSQL`: a CTE per table filtering by `event_time`, then a
   `FULL OUTER JOIN` on `event_id` with four `COUNT(*) FILTER` clauses
   to bucket each row into matched / only-legacy / only-v2 /
   content-diff. Uses `IS DISTINCT FROM` so JSON `context` differences
   compared via `::text` round-trip the way Yugabyte already stores
   them.
2. `parityExamplesSQL`: an INNER JOIN on `event_id` filtered to rows
   where any compared field differs, ordered by `event_id`, limited to
   `parityExampleLimit` (10). Only fired when `counts.ContentDiff > 0`.

Both queries should hit the `events_event_id_uniq` and
`events_v2_event_id_uniq` indexes for the join key.

## Verification

Ran from the repo root.

- `make build`: PASSED. Vet, golangci-lint (0 new findings),
  gocyclo, deadcode, staticcheck-extra (0 new findings),
  govulncheck (0 vulns), and the final
  `go build -tags 'fdb' -o dist/tack ./cmd/server` link all
  succeeded.
- `make lint-diff` against the staged set
  (`internal/ops/audit_parity*.go`, `internal/ops/command.go`,
  `internal/ops/command_help.go`): "golangci-lint: OK (0 findings on
  listed files)" and "staticcheck-extra: OK (0 new findings vs
  baseline)".
- `gofmt -l internal/ops/audit_parity.go internal/ops/audit_parity_test.go
  internal/ops/audit_parity_pgx.go internal/ops/audit_parity_window_test.go`:
  no output (clean).
- `go test -v -count=1 ./internal/ops/`: all 13 new tests PASS, plus
  the 90+ pre-existing repair/backfill tests still PASS.

The `internal/audit` package has a pre-existing, unrelated test
failure (`monitoring_test.go:92`,
`record timeout 750ms is less than allowed 1s`) that is not on the
audit-parity code path. Surfacing here because `make test` returns
non-zero overall on this branch; the author should investigate
separately.

## Deviations from the prompt

- `cmd/server/main.go` did not need a one-line addition (see "Files
  NOT modified" above).
- The instruction said "under 200 lines" for one file; instead split
  the production code into two files (entrypoint and pgx adapter) so
  each stays under 200, and similarly split the tests. This follows
  the project rule "No file should exceed 200 lines. If it does,
  split it" in `CLAUDE.md`.
- The new tests run on the host today. The Docker test runner
  retooling is out of scope for this slice; flagging that
  `internal/ops/audit_parity_test.go` and
  `internal/ops/audit_parity_window_test.go` will need to be
  re-runnable under the upcoming Docker test runner.
