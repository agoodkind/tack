# Phase 1 Compliance Fix Report

Date: 2026-05-09. Files touched: `internal/audit/wal.go`, `internal/audit/wal_test.go`, `internal/telemetry/metrics.go`, `internal/config/config.go`, `cmd/server/main.go`.

## 1. What was removed

From `internal/audit/wal.go`:

- The `gate chan struct{}` field on `WALRecorder` and the `MaxConcurrentReads` field on `WALConfig`. The channel is no longer allocated.
- The `acquireGate` method and the overflow branch in `Record` that called it. `Record` for read verbs now always calls `append` directly.
- The `ErrBackpressure` sentinel.
- The `backpressureWaitTotal` and `backpressureTimeoutTotal` atomic counters and the matching `Stats()` accessors.
- The `BackpressureWait` field on `WALConfig`.

From `internal/telemetry/metrics.go`:

- The `BackpressureWaitTotal` and `BackpressureTimeoutTotal` fields on `WALStatsSource` and `walMetricsSource`.
- The `audit_wal_backpressure_wait_total` and `audit_wal_backpressure_timeout_total` expvar publications.

From `internal/config/config.go`:

- The `AuditWALBackpressureWait` field and the `AUDIT_WAL_BACKPRESSURE_WAIT` env var.
- The `AuditWALMaxConcurrentReads` field and the `AUDIT_WAL_MAX_CONCURRENT_READS` env var.
- The doc comment block that described both fields.

From `cmd/server/main.go`:

- `BackpressureWait` and `MaxConcurrentReads` literals on the `WALConfig` value.
- `BackpressureWaitTotal` and `BackpressureTimeoutTotal` fields on the `WALStatsSource` value passed to `RegisterWALMetrics`.

## 2. What was kept

- Idle rotation is unchanged in shape: the drainer still force-rotates the active segment after `idleRotateAfter`, so the trailing event flushes without waiting for a minute boundary.
- The atomic backlog signals (`unflushedSegments`, `oldestUnflushedUnixNano`, `lastDrainSuccessUnix`) and `idleRotationsTotal` continue to be exported through `WALStats` / `WALStatsSource`. They no longer drive any drop or backpressure decision.
- The former `atOverflow` predicate is renamed `BacklogSignal` and now takes the threshold parameters as arguments. It is called only from tests and (potentially) future telemetry; `Record` no longer consults it.
- Expvar metrics `audit_wal_backlog_segments`, `audit_wal_backlog_oldest_age_seconds`, `audit_wal_last_drain_unix`, and `audit_wal_idle_rotations_total` remain published.

## 3. What was added

In `internal/audit/wal.go`:

- A `statfs` field on `WALRecorder` holding the function pointer to `syscall.Statfs`. The drainer reads it under `statfsMu` so tests can swap a fake probe at runtime without racing.
- `sampleDiskFree`, called once per `drainOnce` cycle. It runs `syscall.Statfs`, stores free bytes in the `diskFreeBytes` atomic, and emits a `Warn` slog `audit.wal.disk_pressure` when free space drops below `max(5% of total, 100 MiB)`. The signal is observability only; it never affects `Record`.
- ENOSPC propagation through the `append` path. Every write-side error (open, write, flush, fsync) now increments `writeErrorsTotal`, emits an `Error` slog `audit.wal.write_failed` (with `enospc` boolean), and returns the wrapped error to the caller.
- Sealed segment names. `rotateActiveLocked` now `os.Rename`s the active file to a `<stem>.<nanos>.wal` sibling before returning. This closes a producer-vs-drainer race that allowed the drainer to remove a file while the producer was reopening the same canonical path.
- A bounded final drain loop in `Close` (up to 8 passes) so that when a force-rotate inside one pass schedules a drain for the next pass, the trailing segment still flushes before `Close` returns.

In `internal/telemetry/metrics.go`:

- `WriteErrorsTotal` and `DiskFreeBytes` fields on `WALStatsSource` and the matching expvar publications `audit_wal_write_errors_total` and `audit_wal_disk_free_bytes`.

In `internal/audit/wal_test.go`:

- `TestBacklogDoesNotDropEvents` (replaces `TestBacklogSegmentsTripsBackpressure`).
- `TestOldSegmentDoesNotDropEvents` (replaces `TestBacklogAgeTripsBackpressure`).
- `TestBacklogClearsAfterDrainerRecovery` (replaces `TestDrainerRecoveryReleasesBackpressure`).
- `TestStateChangeBypassesWAL` (renamed from `TestStateChangeBypassesBackpressure`).
- `TestRecordReturnsErrorOnENOSPC` exercises the ENOSPC path by swapping the active segment's bufio writer with a failing one and asserts both `errors.Is(err, syscall.ENOSPC)` and the `writeErrorsTotal` increment.
- `TestDiskPressureWarningEmitted` injects a fake Statfs reporting near-zero free space and asserts the `audit.wal.disk_pressure` warn line and that `Record` still succeeds.
- A `lockedBuffer` helper that wraps `bytes.Buffer` for the slog capture in the disk-pressure test, so the drainer goroutine and the test goroutine no longer race on `bytes.Buffer.Write` / `bytes.Buffer.String`.
- `writeSegmentWithEvent` helper: empty pre-injected segments are removed by the drainer on first scan without ever calling the inner Recorder, so tests that need the drainer to actually stall write one real event per segment.

## 4. Verification results

All commands run from `/Users/agoodkind/Sites/tack`.

- `make build`: PASS for the WAL refactor scope. There is one unrelated lint failure outside this task. See section 5.
- `make test`: every WAL test passes. The audit package test completes in roughly 40 seconds (well under the prior 600-second timeout). One unrelated test (`TestKafkaRecorderReturnsErrorOnBrokerDown` in `internal/audit/kafka_recorder_test.go`) fails for reasons that have nothing to do with this fix; see section 5.
- `go test -race -count=5 ./internal/audit/...` (run via the explicit `-run` filter for the WAL tests): PASS in roughly 253 seconds, no data races reported.
- Em-dash scan `grep -nP '[\x{2014}\x{2013}]' internal/audit/wal.go internal/audit/wal_test.go internal/telemetry/metrics.go internal/config/config.go cmd/server/main.go`: zero matches.

## 5. Deviations from the prompt

- The prompt limited file scope to `internal/audit/wal.go`, `internal/audit/wal_test.go`, `internal/telemetry/metrics.go`, `internal/config/config.go`, and `cmd/server/main.go`. While I was working another change introduced `internal/audit/kafka_recorder.go` and `internal/audit/kafka_recorder_test.go`. Both fail `make build` (lint) and `make test` independent of my changes. I did not touch those files because the prompt forbids edits under `internal/audit/` outside the WAL pair. The failures are not caused by this fix and not blocked by it.
- The `TestRotationDuringHighThroughput` test as originally written used `for j := 0; j < total/concurrency; j++` with `total = 10_000` and `concurrency = 32`. Integer division gives 312 iterations per goroutine, so the loop appends 9984 events but the assertion compared to `total = 10_000`. The arithmetic is wrong by construction; with the gate removed, this discrepancy started failing the test with a "16 missing events" report that had nothing to do with data loss. I changed the test to declare `perGoroutine = 312` and `total = concurrency * perGoroutine = 9984` so that `appended` matches the number of events actually submitted. Behavior under test is unchanged; the assertion is now arithmetically correct.
- The prompt asked for a "soft pre-check" Statfs once per `drainOnce` cycle. I implemented that as `sampleDiskFree`. The prompt also asked to "surface `syscall.ENOSPC` as the error" inside `ensureSegmentLocked` or `append`. I did not add a separate ENOSPC-only branch; instead `recordWriteError` is called for every write-side failure and includes an `enospc` boolean attribute on the slog line so the operator alert is keyable. The error returned to the caller is the wrapped underlying error, so `errors.Is(err, syscall.ENOSPC)` works as the prompt requires.
- The drainer now renames force-rotated segments to a unique sealed name. This is a defensive change that surfaced once the gate was removed, because producers can now race with the drainer at full speed and the previous canonical-name reuse pattern dropped writes. The change keeps generic behavior generic: callers see no contract change.
- Tests that previously injected zero-byte segments to simulate a backlog were updated to inject one real event per segment. Empty segments hit EOF immediately and the drainer removes them without ever calling the inner Recorder; that meant the "blocking inner" setup did not actually stall the drainer in the new architecture. The fix uses `writeSegmentWithEvent` for those tests so the blocking semantics actually apply.
