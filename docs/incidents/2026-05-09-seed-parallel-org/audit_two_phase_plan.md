# Audit Two-Phase Plan

## 1. Phase Summaries

**Phase 1 (now).** Patch the existing `WALRecorder` so the active segment cannot age past the overflow threshold without rotating, replace the filename-age overflow check with a real backlog-rows-and-bytes signal, and convert silent drop-on-overflow into bounded backpressure that surfaces a loud error to the caller. Schema, Yugabyte path, notarizer, MCP audit tools, and the state-change-verb synchronous bypass are all preserved unchanged. Pre-flight drains the stuck `20260509T055731.wal` segment via a graceful restart before the new binary ships. Deployable in one maintenance window; rollback is a binary swap with zero data loss.

**Phase 2 (later, design only).** Replace the local-disk WAL with a single-broker Redpanda service inside the existing CT 117 Compose stack, keeping `Recorder.Record(ctx, ev)` byte-compatible for callers. Producer becomes a Kafka-protocol producer that writes one canonical `audit.events.v1` topic; a new `audit-consumer` binary reads the topic, computes the hash chain in `audit.chain_heads`, projects rows into `audit.events`, and runs the notarizer. State-change verbs continue to bypass the broker and write straight to Yugabyte. A dual-write parity window gates cutover. Backups, multi-AZ, and security are explicitly tracked as open follow-ups under the broker design.

## 2. Phase 1 Detailed Plan

### 2a. Decision: rotation approach

**Chosen: include the active segment in `drainOnce` once it ages past `drainInterval`, draining and rotating it under the same `w.mu` lock the appender uses.**

The mechanism: in `drainOnce` (`/Users/agoodkind/Sites/tack/internal/audit/wal.go:283`), when iterating segments, check whether the active segment (`activeName`) is older than `idleRotateAfter` (default `2 * w.drainInterval`, i.e. 500 ms). If yes, take `w.mu`, call a new `rotateActiveLocked` that flushes-and-closes the active segment and clears `w.current`, release `w.mu`, then drain the now-non-active file in the same loop iteration. The next `Record` call lazily re-opens a fresh segment via `ensureSegmentLocked` (`wal.go:188`) using the current minute name, so no producer-side change is required.

Why this approach:

- Single source of truth for "what gets drained": the drainer. No second timer goroutine to reason about.
- Closes the exact bug at `wal.go:283-320`: `drainOnce` already skips `activeName`; we are removing that skip after the segment goes idle. The minimal viable change.
- The lock ordering stays the same the appender uses, so we cannot race a partial write.

Rejected alternative A: periodic forced rotation in a second goroutine. Adds a third moving part (appender, drainer, rotator) and a third place where a future bug can land. Two goroutines have to agree on `w.current` and on `w.mu`. The drainer already holds the right lock once per `drainInterval`; reusing it is strictly less code.

Rejected alternative B: `atOverflow` ignores the file currently bound to `w.current`. This makes `atOverflow` lie when the active segment really is the backlog (which is the bug today), and it still does not cause the active segment to be drained, so 4 events stay buffered indefinitely. It hides the symptom rather than fixing the cause.

### 2b. Decision: backlog signal and threshold

**Chosen signal: count of unflushed segments PLUS oldest-event age inside the unflushed segments, computed at scan time during `drainOnce` and cached in two atomics consulted by `atOverflow` from the producer.**

Rationale: the failure mode we keep losing data on is "drainer is not making progress." Either the queue grew (segments accumulated) or the head of the queue got old (segments stayed). Filename age (current code) only sees the second, and it sees it for the wrong file (the active one). The fix needs both:

- `unflushedSegments`: `int32` atomic, count of `*.wal` files in the WAL dir that are NOT the active segment. Updated at the top of every `drainOnce`.
- `oldestUnflushedUnixNano`: `int64` atomic, parsed from the lex-min non-active segment filename, or `0` when none. Updated at the top of every `drainOnce`.

Default thresholds:

- `MaxBacklogSegments = 64`. Each segment is at most one minute of traffic (segment names are `YYYYMMDDTHHMMSS.wal`, see `wal.go:227-229`). 64 segments equals roughly 64 minutes of buffered traffic. At observed peak rates (tens of read events per second) this is a few hundred thousand buffered events; at design rates (millions per second) the backlog crosses the rows threshold first. Set this at "operator probably needs to know" rather than "operator needs to scream now."
- `MaxBacklogAge = 10 * time.Minute`. Today's `MaxLag` default is 30 minutes. We tighten by 3x because the active segment is now also drained, so a real 10-minute backlog of non-active segments means the drainer is genuinely failing, not just sitting on a slow minute. Ten minutes is also short enough to fire well before the 60 minutes of YB-side notarization-signing latency the operator already alerts on.

Either signal alone trips overflow. The producer's `atOverflow` becomes a pure atomic load:

```
return w.unflushedSegments.Load() > w.maxBacklogSegments ||
       (w.oldestUnflushedUnixNano.Load() != 0 &&
        time.Now().UnixNano() - w.oldestUnflushedUnixNano.Load() > int64(w.maxBacklogAge))
```

No filesystem I/O on the hot path. Today's `atOverflow` does a `ReadDir` on every read-class Record (`wal.go:234`), which is itself a per-event syscall on the hot path. Removing it is a separate latency win.

The active segment is intentionally excluded from both signals because the rotation fix in 2a guarantees it ages out into the unflushed set within `idleRotateAfter`. Once it is unflushed, both signals see it normally.

### 2c. Decision: backpressure mechanism

**Chosen: bounded blocking on a buffered channel acting as a write semaphore, with a configurable per-call timeout (`BackpressureWait`) defaulting to 250 ms.**

The mechanism: a `w.gate chan struct{}` of capacity `MaxConcurrentReads` (default `64`) is created at `NewWALRecorder`. Inside `Record`, before `w.append`, the producer:

1. Checks `atOverflow()` cheaply via the atomics from 2b.
2. If overflow is signaled, the producer attempts `select { case w.gate <- struct{}{}: ... case <-time.After(w.backpressureWait): ... case <-ctx.Done(): ... }`.
3. On success, the slot is released by `defer func(){ <-w.gate }()` after `append` returns.
4. On timeout, the producer returns `audit.ErrBackpressure` (a sentinel exported by the package). `SuppressingRecorder.Record` (`internal/audit/context.go:97-107`) already wraps the inner error and propagates it to the caller. There is no silent drop path.
5. On `ctx.Done()`, return `ctx.Err()`. State-change verbs do not pass through this code path (they bypass at `wal.go:118-120`), so backpressure does not block them.

Wait timeout rationale: 250 ms is the same magnitude as `drainInterval` (current default 250 ms, `wal.go:92-94`). One drain cycle is the natural relief window; if the drainer cannot drain one segment in one cycle the producer should fail loud rather than queue indefinitely. 250 ms is short enough that an inbound HTTP read does not stack request timeouts on top of audit wait, and long enough that a single transient spike does not surface to the user.

Capacity of 64 was picked because the appender holds `w.mu` per write and fsyncs serially; 64 in-flight write attempts is more than the appender can plausibly serve concurrently anyway. The semaphore exists primarily to make backpressure observable, not to expand throughput.

`audit.ErrBackpressure` is new and must be a sentinel so callers (and the existing `SuppressingRecorder`) can choose to fail-closed for state-change-class verbs. State-change verbs already bypass, so this only matters if a future caller decides to route a state-change verb through the WAL; the explicit error keeps that bug-class loud.

### 2d. Code changes

| File | Target lines | Change |
|---|---|---|
| `/Users/agoodkind/Sites/tack/internal/audit/wal.go` | 36-59 | Add fields `unflushedSegments atomic.Int32`, `oldestUnflushedUnixNano atomic.Int64`, `maxBacklogSegments int`, `maxBacklogAge time.Duration`, `backpressureWait time.Duration`, `idleRotateAfter time.Duration`, `gate chan struct{}`, `lastDrainSuccessUnix atomic.Int64`. |
| `/Users/agoodkind/Sites/tack/internal/audit/wal.go` | 70-75 | `WALConfig` gains `MaxBacklogSegments int`, `MaxBacklogAge time.Duration`, `BackpressureWait time.Duration`, `IdleRotateAfter time.Duration`, `MaxConcurrentReads int`. |
| `/Users/agoodkind/Sites/tack/internal/audit/wal.go` | 79-109 | `NewWALRecorder` defaults the new fields, allocates `w.gate`. |
| `/Users/agoodkind/Sites/tack/internal/audit/wal.go` | 114-131 | `Record` replaces silent drop with semaphore acquire under timeout. Returns `ErrBackpressure` on timeout. |
| `/Users/agoodkind/Sites/tack/internal/audit/wal.go` | 233-252 | `atOverflow` becomes an atomic-only check; the file-walking logic moves into `drainOnce`'s scan. |
| `/Users/agoodkind/Sites/tack/internal/audit/wal.go` | 283-320 | `drainOnce` updates `unflushedSegments`, `oldestUnflushedUnixNano`, `lastDrainSuccessUnix`. After scan, if active segment older than `idleRotateAfter`, calls a new `rotateActiveLocked` that flush-and-closes, then drains it as a normal non-active segment in the same loop iteration. |
| `/Users/agoodkind/Sites/tack/internal/audit/wal.go` | new | Add `var ErrBackpressure = errors.New("audit wal: backpressure timeout")`. |
| `/Users/agoodkind/Sites/tack/internal/audit/wal.go` | 211-223 | `segment.flushAndClose` unchanged. New `(w *WALRecorder) rotateActiveLocked() error` reuses it. |
| `/Users/agoodkind/Sites/tack/internal/telemetry/metrics.go` | adjacent to existing `auditRecords`, `auditDropped` | Add gauges `audit_wal_backlog_segments`, `audit_wal_backlog_oldest_age_seconds`, `audit_wal_last_drain_unix`, `audit_wal_backpressure_wait_total` (counter), `audit_wal_backpressure_timeout_total` (counter). Expose via `expvar.Func` reading the atomics on `WALRecorder` through a small accessor on the package. |
| `/Users/agoodkind/Sites/tack/cmd/server/main.go` | 332-337 | `WALConfig` literal in `buildAuditRecorder` populates new fields from config, defaults still pull through `NewWALRecorder`. No env vars required to ship; defaults are safe. |
| `/Users/agoodkind/Sites/tack/internal/config/config.go` | adjacent to `AuditWALDir` (line 72) | Optional new env: `AUDIT_WAL_MAX_BACKLOG_SEGMENTS`, `AUDIT_WAL_MAX_BACKLOG_AGE`, `AUDIT_WAL_BACKPRESSURE_WAIT`, `AUDIT_WAL_IDLE_ROTATE_AFTER`. All optional; absence keeps Phase 1 defaults. |

No changes to: `/Users/agoodkind/Sites/tack/internal/audit/yugabyte.go`, `/Users/agoodkind/Sites/tack/internal/audit/notarizer.go`, `/Users/agoodkind/Sites/tack/internal/audit/canonical.go`, `/Users/agoodkind/Sites/tack/internal/audit/recorder.go`, `/Users/agoodkind/Sites/tack/internal/audit/context.go`, `/Users/agoodkind/Sites/tack/internal/audit/verbs.go`, `/Users/agoodkind/Sites/tack/migrations/002_audit.sql`, MCP audit tooling. Phase 1 is opaque to every caller of the audit package except for the new `ErrBackpressure` sentinel which existing callers do not need to handle specially.

### 2e. New tests (`/Users/agoodkind/Sites/tack/internal/audit/wal_test.go`)

| Test | What it exercises | Why current tests miss it |
|---|---|---|
| `TestIdleSegmentRotates` | Open `WALRecorder` with `IdleRotateAfter=20ms`, `DrainInterval=10ms`. Append one event, sleep 100 ms with no further writes. Assert the active segment file no longer exists in the dir, the event landed in the inner `MemoryRecorder`, and `unflushedSegments` is 0. | Existing `wal_test.go` only flushes via Append-driven rotation; idle rotation is never triggered. |
| `TestBacklogSegmentsTripsBackpressure` | Inject 65 fake closed segments into the WAL dir before `NewWALRecorder`, configure `MaxBacklogSegments=64` and a slow inner recorder that blocks. Append a read-class event. Assert `Record` returns `ErrBackpressure` after `BackpressureWait`, and the metric `audit_wal_backpressure_timeout_total` increments. | Today's tests never simulate a stuck drainer because `atOverflow` only looks at filename age. |
| `TestBacklogAgeTripsBackpressure` | Inject one segment with a filename timestamp older than `MaxBacklogAge`, plus a slow inner recorder. Same assertion as above. | Today's overflow test path uses the same filename-age signal but never exercises the new "loud failure" semantic. |
| `TestDrainerRecoveryReleasesBackpressure` | Trip backpressure via slow inner; unblock the inner; assert the next `Record` call succeeds within one `DrainInterval` and `oldestUnflushedUnixNano` returns to zero. | Recovery from backpressure has no current coverage. |
| `TestStateChangeBypassesBackpressure` | Trip backpressure (slow inner). Issue `Record` for `VerbNodeCreate`. Assert it goes synchronously to the inner without consulting overflow signals or the gate. | Confirms requirement 4 (state-change verbs not exposed to backpressure) does not regress. |
| `TestRotationDuringHighThroughput` | Append 10k events at concurrency 32 with `IdleRotateAfter=1ms`. Assert no panic, no lost events, no segment that exceeds `MaxBytesPerSegment`, drainer keeps up. | The current concurrent-access tests do not interleave idle rotation with hot writes. |
| `TestPreflightDrainOnRestart` | Pre-populate WAL dir with one segment containing 4 events at oldest filename, no active segment. `NewWALRecorder` plus a one-shot drain plus `Close` flushes all 4 to inner. | Mirrors the production pre-flight step; today no test asserts that `drainOnce` will pick up an existing segment without further appends. |

### 2f. New metrics and slog lines

Metrics (registered in `/Users/agoodkind/Sites/tack/internal/telemetry/metrics.go`, alongside existing `auditRecords` at line 28):

- `audit_wal_backlog_segments` (gauge, expvar Func): unflushed segment count.
- `audit_wal_backlog_oldest_age_seconds` (gauge, expvar Func): seconds since the oldest unflushed segment's filename timestamp; 0 when no backlog.
- `audit_wal_last_drain_unix` (gauge, expvar Func): unix seconds of the most recent successful drain.
- `audit_wal_backpressure_wait_total` (counter): how often a producer call entered the gated wait.
- `audit_wal_backpressure_timeout_total` (counter): how often the gated wait timed out and `ErrBackpressure` was returned.
- `audit_wal_idle_rotations_total` (counter): how often the drainer force-rotated an idle active segment.

Slog lines (channel name `audit.wal.*` consistent with existing lines):

- `audit.wal.idle_rotated` (Info): emitted from `drainOnce` after a forced idle rotation. Fields `path`, `idle_ms`.
- `audit.wal.backpressure_engaged` (Warn): emitted when a producer call enters the gated wait. Fields `verb`, `backlog_segments`, `oldest_age_seconds`.
- `audit.wal.backpressure_timeout` (Error): emitted when the wait times out. Same fields plus `wait_ms`.
- `audit.wal.drain_failed` (Error): replaces the silently-swallowed `_` in the existing `os.Remove` path at `wal.go:388-394` (already an error log; we promote any swallow site noticed during the change to the same channel).

Removed silent path: the `WARN audit.wal.dropped` plus `IncAuditDropped(verb, "wal_overflow")` at `wal.go:121-127` is deleted. The drop counter remains in the codebase for non-overflow drop reasons (marshal failure, etc.) so existing dashboards do not break.

### 2g. Pre-flight: drain `20260509T055731.wal`

Goal: get the 4 buffered events into `audit.events` before the new binary touches the WAL dir, so the new code never inherits a "what about these legacy bytes" ambiguity.

Steps, all reversible:

1. On CT 117, capture a snapshot before any change: `tar czf /root/incident_2026-05-09_seed_parallel_org/audit-wal-preflight-$(date -u +%Y%m%dT%H%M%SZ).tar.gz /var/lib/tack/audit-wal/`. Do not delete the original.
2. Capture the `audit.events` row count and `MAX(event_time)` before restart. Expected to read 342,166 (342,162 historical plus 4 about to land) after the restart completes.
3. Graceful restart of `tack-app-1`: `docker compose -f /root/tack/docker-compose.yml restart app`. The `Close` path at `/Users/agoodkind/Sites/tack/internal/audit/wal.go:137-153` flushes the active segment AND runs `drainOnce` with no active segment, so the file at `/var/lib/tack/audit-wal/20260509T055731.wal` is replayed via `drainSegment` (`wal.go:322-406`) to the inner `YBRecorder`. Each event surfaces as `audit.reconciler.flushed` in the slog and lands in `audit.events`.
4. Verify: `docker compose exec yugabyte ysqlsh ... -c "SELECT count(*) FROM audit.events"` returns 342,166. `ls /var/lib/tack/audit-wal/` returns empty or only a fresh post-restart segment with size 0. The four flushed events should be visible by `SELECT action, event_time FROM audit.events WHERE event_time BETWEEN '2026-05-09 05:57:00' AND '2026-05-09 05:58:00'` returning 4 rows whose verbs match the `audit.wal.dropped` log lines from the incident window.
5. If the restart does not flush, abort. Diagnose with `docker logs tack-app-1 | grep -i 'audit.wal'`. Do not deploy Phase 1 binary on top of an undrained legacy segment because the new `oldestUnflushedUnixNano` would immediately read minutes-old, trip backpressure on first read, and surface as a regression rather than the recovery it is.

Pre-flight runs on the existing buggy binary. Phase 1 binary deploy follows step 5 success.

### 2h. Deployment sequence and rollback

| Step | Action | Reversible by | Gate |
|---|---|---|---|
| 0 | Pre-flight (2g) | n/a | 4 events landed; WAL dir empty |
| 1 | Build, test (full `go test ./...`), tag binary `tack-server:phase1-wal-fix` | redeploy previous tag | All Phase 1 tests pass; existing tests still pass |
| 2 | Deploy: `docker compose pull && docker compose up -d app` (image tag bumped in `.env`) | redeploy previous tag with `docker compose up -d app` | Container starts, `audit.writer_connected`, `audit.wal_enabled` log lines emit |
| 3 | Hit a read-class verb via MCP from a separate session | revert step 2 | New `audit.wal.appended` log line at Debug, `audit.events` count climbs |
| 4 | Wait 30 minutes idle (no MCP traffic) then issue another read | revert step 2 | `audit.wal.idle_rotated` log line appears in the gap; `audit.wal.appended` for the post-gap event lands; no `audit.wal.dropped` ever |
| 5 | Soak 24 hours, watch metrics | revert step 2 | `audit_wal_backlog_segments` rarely above 1; `audit_wal_backpressure_timeout_total` zero; `audit_wal_last_drain_unix` always under 30 s old |

Rollback path: redeploying the previous binary at step 2 leaves the WAL dir on disk. The previous binary will see the new fresh segment, drain it normally on its next `drainOnce`, and resume the original (still-buggy) behavior. No schema changed, no on-disk format changed, no env var must be set or unset. Rollback is byte-compatible because the new code writes the same length-prefixed JSON segment format the old code already understands (`wal.go:155-186`). One consequence: any segment recently rotated by the new code's idle-rotation has filename timestamp earlier than the active minute, but the previous binary's `drainOnce` already handles older-named non-active segments correctly (`wal.go:303-319`). The pre-flight snapshot from 2g is the absolute fallback: untar to `/var/lib/tack/audit-wal/` and the previous binary runs identically to the broken state we started from.

### 2i. Verification (post-deploy)

In order, the operator should observe each:

1. Within the first minute, `audit.wal.appended` Debug lines and `audit.reconciler.flushed` Info lines for at least one drained segment. Existing log channels, current operator dashboards already have them.
2. `expvar` at `/debug/vars` exposes `audit_wal_backlog_segments` (gauge, currently 0 or 1), `audit_wal_last_drain_unix` (gauge, within last 30 s), `audit_wal_backpressure_wait_total` (counter, ideally 0), `audit_wal_backpressure_timeout_total` (counter, must remain 0 in a healthy state).
3. After 30 minutes of intentional silence, an `audit.wal.idle_rotated` log line appears once (or more, if multiple idle windows occur). This is the structural fix.
4. `SELECT count(*) FROM audit.events` continues to climb in proportion to inbound MCP traffic, with no minute-long flat plateaus. Compare to a sliding-window MCP request counter; the ratio should be roughly 1:1 for read-class verbs.
5. `audit.wal.dropped` never appears in slog after the new binary lands. Its absence is the proof that silent drops are gone.
6. Failure-mode rehearsal: temporarily point `AUDIT_WRITER_DSN` at a closed port via env override, run a few read-class MCPs. Expected: `audit.wal.replay_failed` ERROR lines, `audit_wal_backlog_segments` climbs as the drainer cannot ship to YB, after a few minutes `audit_wal_backpressure_wait_total` climbs, then `audit_wal_backpressure_timeout_total` climbs and producer calls return `ErrBackpressure`. Restore the DSN and confirm both gauges drift back to 0 within minutes. Document this rehearsal in the runbook so the next incident has known telemetry.

## 3. Phase 2 Detailed Plan

### 3a. Decision: Kafka vs Redpanda

**Chosen: Redpanda (single binary, BSL-licensed Community Edition).**

Decision criteria, in order:

1. **Single-binary footprint on CT 117.** CT 117 is one host running Compose. Apache Kafka without ZooKeeper still requires a Kafka broker process and the operator must learn KRaft quorum management. Redpanda is one statically linked binary with no JVM, no ZooKeeper, no separate controller, and a single config file. For a single-host single-broker target this is the smaller blast radius.
2. **Operational drag.** Redpanda exposes the Kafka wire protocol so Go clients (`segmentio/kafka-go`, `twmb/franz-go`) work unmodified. Operator does not learn JMX. `rpk` is the one CLI for everything.
3. **Multi-tenancy.** Both brokers do per-topic ACLs and per-topic retention. Equivalent for our needs; we run one topic per audit class.
4. **Retention configuration.** Redpanda's tiered storage is irrelevant for single-host; topic-level `retention.ms` plus `retention.bytes` is identical to Kafka.
5. **Backup procedure.** Both write segment files; `rpk` has a documented snapshot path (`rpk cluster storage snapshot` plus `rpk topic backup` workflows) and Kafka requires either MirrorMaker 2 or third-party tooling. Redpanda's docs are shorter, which matters for a one-operator team.
6. **On-prem deployability under Compose.** Both have official images. The Redpanda image has fewer JVM-shaped tunables to mistune.
7. **Licensing.** Redpanda Community Edition (BSL with conversion to Apache 2.0 after four years) is acceptable for self-hosted compliance use. Kafka is Apache 2.0. Not a tiebreaker; both are usable.

Tiebreaker: Redpanda. Net rationale: the smallest binary, smallest config surface, smallest operator-learning curve for the single-broker reality on CT 117. The decision is reversible because the wire protocol matches Kafka; the producer code does not change if we later swap brokers.

### 3b. Target architecture

```
       producer (tack-app)
       Recorder.Record(ctx, ev)
                |
                v
       audit.RecorderRouter
        |                |
        v                v
   state-change?     read-class?
   (verbs.go:63)     (else)
        |                |
        v                v
   YBRecorder      KafkaRecorder
   (sync to YB)    (produce to topic)
                            |
                            v
                  redpanda broker (CT 117 Compose)
                  topic: audit.events.v1
                  partitions: 8
                  retention: 7 days or 50 GiB
                            |
                            v
                  audit-consumer binary (CT 117 Compose)
                  - subscribes by group `audit-projector`
                  - per message:
                      compute shard, prev_hash, row_hash
                      INSERT audit.events
                      UPDATE audit.chain_heads
                      commit Kafka offset in same YB txn via
                      transactional outbox row (audit.consumer_offsets)
                  - notarizer subroutine (60s tick)
                            |
                            v
                  YugabyteDB
                  audit.events  (unchanged)
                  audit.chain_heads (unchanged)
                  audit.notarizations (unchanged)
                  audit.pii (writer side: producer still writes,
                             dual-write during migration window)
```

Component responsibilities:

- `RecorderRouter` (new file `/Users/agoodkind/Sites/tack/internal/audit/router.go`): tiny dispatch on `IsStateChange(Verb(ev.Verb))` (`verbs.go:84`). State-change goes to `YBRecorder`. Read-class goes to `KafkaRecorder`. This replaces the role `WALRecorder` plays today as the read-vs-state branching point. The branching is already present at `wal.go:118-120`; we are just lifting it out of the WAL implementation.
- `KafkaRecorder` (new file `/Users/agoodkind/Sites/tack/internal/audit/kafka_recorder.go`): one Kafka producer per process, sync produce with `acks=all`, JSON-encoded `Event` payload identical to the WAL on-disk format (`wal.go:155-186`). Returns the producer error to the caller on failure; no silent swallow.
- `audit-consumer` binary (new dir `/Users/agoodkind/Sites/tack/cmd/audit-consumer/`): franz-go consumer in group `audit-projector`. Per record: deserialize `Event`, compute `shardOf(actor, eventID)`, run the same hash logic as `yugabyte.go:152-209`, INSERT events + chain_heads + offsets in one YB transaction. Notarizer goroutine inside same binary.
- `audit.consumer_offsets` (new table, migration `005_audit_consumer_offsets.sql`): `(topic TEXT, partition INT, offset BIGINT, updated_at TIMESTAMPTZ, PRIMARY KEY (topic, partition))`. Updated transactionally with each event INSERT so consumer crashes cannot double-commit.

### 3c. Topic design

- Topic name: `audit.events.v1`. The `.v1` suffix lets a future schema break ship as `audit.events.v2` while the consumer keeps reading the previous topic.
- Partitions: 8. Matches the `audit.events` parent partition layout (8 weekly partitions visible in the migration `002_audit.sql:86-95`). Partition key is `org_id` so events for one tenant are processed in order.
- Replication factor: 1 (single-host CT 117 reality). Documented as a known compromise; replication factor 3 is the post-multi-host default but requires more than one broker.
- Retention: `retention.ms = 604800000` (7 days), `retention.bytes = 53687091200` (50 GiB). Either-trips-first wins. This is intentionally generous: the consumer must keep up under all normal conditions, and the broker is a buffer not a long-term store. Long-term storage stays in `audit.events`.
- Compression: `lz4` per-batch.
- Cleanup policy: `delete` (not compacted). Audit events are append-only and we never reuse keys.

### 3d. Producer code surface

`Recorder.Record(ctx, ev)` interface unchanged (`recorder.go:23-25`). Concrete change:

```go
type KafkaRecorder struct {
    cl    *kgo.Client
    topic string
}

func (r *KafkaRecorder) Record(ctx context.Context, ev Event) error {
    if ev.OccurredAt.IsZero() { ev.OccurredAt = time.Now().UTC() }
    payload, err := json.Marshal(ev)
    if err != nil { return fmt.Errorf("audit kafka marshal: %w", err) }
    rec := &kgo.Record{
        Topic: r.topic,
        Key:   ev.Context.OrgID[:],
        Value: payload,
    }
    return r.cl.ProduceSync(ctx, rec).FirstErr()
}
```

Failure mode: synchronous produce with `acks=all` returns the broker error. `SuppressingRecorder` propagates it. State-change verbs go to `YBRecorder` directly via `RecorderRouter` and never see the broker, so a broker outage cannot affect state-change writes.

PII handling under Phase 2: producer continues to write the PII row to `audit.pii` synchronously (we already do it inside `YBRecorder.Record` at `yugabyte.go:113-134`). Phase 2 moves the PII insert ahead of the Kafka produce so the `pii_ref` is a valid row before the consumer reads the event. This requires one shared YB pool on the producer for `audit.pii` writes only; same pool footprint as today.

Caller-visible behavior: identical. `Recorder.Record(ctx, ev)` still either returns nil or an error. No new sentinel needed because the broker either accepts or rejects; `ErrBackpressure` from Phase 1 is removed once the Kafka path replaces the WAL.

### 3e. Consumer code surface

`/Users/agoodkind/Sites/tack/cmd/audit-consumer/main.go` (new):

- Opens franz-go client in consumer group `audit-projector`.
- Opens YB pool via `AUDIT_WRITER_DSN` (same role today's `YBRecorder` uses).
- Loop: poll batch, for each record start a YB transaction, run the same hash chain logic as `yugabyte.go:138-209`, INSERT into `audit.events`, UPDATE `audit.chain_heads`, UPSERT into `audit.consumer_offsets`, commit YB, then commit Kafka offset.
- Notarizer goroutine: identical to today's `Notarizer` (`notarizer.go:90-199`), reads `chain_heads`, writes `notarizations`. Same Ed25519 signing, same minute tick, same Merkle output. The path lives inside the consumer process so notarization sees a coherent post-projection chain head, but the public `audit.Notarizer` Go type is preserved for any tests that call it directly.
- Signal handling: SIGTERM stops the poll loop, drains in-flight, commits final offset, closes YB pool. Idempotent.

Hash chain semantics (requirement 3 of Phase 2): unchanged. The consumer calls `hashRow` (`canonical.go:97`) with the same payload shape (`yugabyte.go:155-170`). `audit.chain_heads` keeps the same `(org_id, shard)` PK. Verifiers and the existing `Reader` see byte-identical chain bytes.

### 3f. Backup story for the broker

Lessons from retro_log.md section 1A: a "backed up" tarball that was never tested ended up empty. Phase 2 must not repeat that pattern.

Plan:

- The broker's segment directory `/var/lib/redpanda/data` is mounted to a named volume `tack-redpanda-data`.
- `/Users/agoodkind/Sites/tack/scripts/backup.sh` adds a step that, with the broker running, runs `rpk topic describe audit.events.v1 -p 0,1,2,3,4,5,6,7 -d > $DEST/redpanda-state.json` (live introspection) AND `tar czf $DEST/redpanda-data.tar.gz -C /var/lib/redpanda data`.
- Restore-test step in CI: a nightly job restores the latest `redpanda-data.tar.gz` into a fresh container, asserts `rpk topic list` returns `audit.events.v1` with the expected partition count, and asserts the latest offset matches the production `audit.consumer_offsets` snapshot from the same backup minute. This is the gating test that catches an empty-tarball defect within 24 hours, not 14 days.
- Because the broker is ahead of YB in the data-flow (consumer commits to YB after Kafka), the YB ledger is the durable copy of record. A broker rebuild from empty is acceptable as long as `audit.consumer_offsets` is preserved: the producer keeps writing, the consumer resumes from the recorded offset (or `latest` if the offset is past the broker's earliest), and the only loss window is "events produced and not yet consumed at the moment the broker died." The consumer-offsets table closes that window for committed events; the remaining gap is bounded by produce-to-commit latency (subsecond in steady state).
- The `audit.events` table itself remains the long-term archive and is covered by the existing YB backup path.

### 3g. Migration from Phase 1 to Phase 2

Two waves, each individually deployable, mirroring the parity model from the prior YB-table plan but adapted to the broker target.

Wave A (dual-write, single-read):

1. Deploy Redpanda Compose service, single broker, single replica.
2. Apply migration `005_audit_consumer_offsets.sql` (new table) and `006_audit_events_event_id_uniq.sql` (UNIQUE index on `event_id`, idempotent reproject) to YB.
3. Build `tack-server` with `RecorderRouter` plus `DualReadRecorder` (new): for read-class verbs, write to BOTH `WALRecorder` (Phase 1 path) AND `KafkaRecorder`. For state-change verbs, write only to `YBRecorder` as today.
4. Deploy `audit-consumer` binary writing to `audit.events_v2` (new sibling table, schema-identical via `CREATE TABLE LIKE`) so consumer-side projection is observed in isolation.
5. Soak 7 days. Parity gate: `(event_id, row_hash)` must match between `audit.events` (WAL path) and `audit.events_v2` (Kafka path) for every event in the window. Drift greater than zero after 5 minutes means abort.

Wave B (cutover):

1. Switch consumer to project into `audit.events` directly (the canonical table). The consumer already deduplicates on `event_id` via the UNIQUE index from migration 006.
2. Switch `RecorderRouter` to `KafkaRecorder` only for read-class verbs. Stop writing to the WAL.
3. Remove `WALRecorder` from `cmd/server/main.go`. Remove `AUDIT_WAL_DIR` and the WAL volume mount from `docker-compose.yml`.
4. After 7 clean days, drop `audit.events_v2` (cleanup migration).
5. Delete `/Users/agoodkind/Sites/tack/internal/audit/wal.go` and `wal_test.go`.

Rollback gates: Wave A rollback is "stop the consumer; flip producer back to WAL-only mode." Phase 1 keeps working. Wave B rollback is "redeploy Wave A binary." The dual-write window in Wave A is the safety net.

### 3h. Operational additions

`/Users/agoodkind/Sites/tack/docker-compose.yml`:

```yaml
  redpanda:
    image: redpandadata/redpanda:v24.2.4
    restart: unless-stopped
    command:
      - redpanda
      - start
      - --overprovisioned
      - --smp 1
      - --memory 1G
      - --reserve-memory 0M
      - --node-id 0
      - --check=false
      - --kafka-addr PLAINTEXT://0.0.0.0:9092
      - --advertise-kafka-addr PLAINTEXT://redpanda:9092
      - --rpc-addr 0.0.0.0:33145
      - --advertise-rpc-addr redpanda:33145
    ports:
      - "9092:9092"
      - "9644:9644"
    volumes:
      - tack-redpanda-data:/var/lib/redpanda/data
    healthcheck:
      test: ["CMD", "rpk", "cluster", "health", "--exit-when-healthy"]
      interval: 10s
      timeout: 5s
      retries: 6

  audit-consumer:
    image: tack-server:latest
    restart: unless-stopped
    command: ["/usr/local/bin/audit-consumer"]
    environment:
      AUDIT_KAFKA_BROKERS: redpanda:9092
      AUDIT_KAFKA_TOPIC: audit.events.v1
      AUDIT_WRITER_DSN: ${AUDIT_WRITER_DSN}
      AUDIT_SIGNING_KEY_PATH: /etc/tack/audit-signing.pem
    volumes:
      - /etc/tack:/etc/tack:ro
    depends_on:
      redpanda: { condition: service_healthy }
      yugabyte: { condition: service_healthy }
```

New env vars on `app` service: `AUDIT_KAFKA_BROKERS`, `AUDIT_KAFKA_TOPIC`. `AUDIT_WAL_DIR` retained during Wave A, removed in Wave B.

New named volume: `tack-redpanda-data`.

### 3i. Open questions explicitly punted

- **Multi-AZ.** CT 117 is single-host; replication factor 1 is a known compromise. When Tack acquires a second host, the broker becomes a 3-replica cluster across hosts and partitions get rebalanced. The Phase 2 design does not solve this; it documents the gap.
- **Replication factor for `audit.consumer_offsets`.** Lives in YB which inherits whatever YB replication is configured. Same single-host compromise.
- **Security model.** Phase 2 ships with PLAINTEXT listener inside the Compose network. mTLS between producer and broker, plus broker SASL/SCRAM, is a follow-up and explicitly punted to Phase 3 design.
- **Upgrade procedure.** Redpanda supports rolling upgrade with multi-broker but on a single broker an upgrade is a full restart with downtime. The producer must tolerate broker unavailability; today it would surface as `Recorder.Record` errors. Acceptable for state-change verbs (they bypass the broker) and acceptable for read-class verbs only if the operator schedules upgrades in maintenance windows. Documented gap.
- **Replay tooling.** Operators need a way to manually re-emit a range of events from Kafka into `audit.events` without re-running the whole consumer (e.g., during forensic review). The `rpk topic consume` plus a small replay tool is sketched, not built. Punted.

## 4. Critical Files

### Phase 1, modified

- `/Users/agoodkind/Sites/tack/internal/audit/wal.go`: rotation, atomic backlog signals, gate-based backpressure, new sentinel, telemetry hooks.
- `/Users/agoodkind/Sites/tack/internal/audit/wal_test.go`: seven new tests covering idle rotation, both backlog trips, recovery, state-change bypass, concurrent rotation, pre-flight drain.
- `/Users/agoodkind/Sites/tack/internal/telemetry/metrics.go`: register the five new counters and gauges plus their accessors.
- `/Users/agoodkind/Sites/tack/cmd/server/main.go` lines 316-347: pass new `WALConfig` fields from config.
- `/Users/agoodkind/Sites/tack/internal/config/config.go`: optional new env vars with safe defaults.

### Phase 1, created

- None. Phase 1 is intentionally additive-within-existing-files only, except for one new `errors.go`-style symbol inside `wal.go`.

### Phase 1, deleted

- None. The `WALRecorder` is preserved.

### Phase 2, created

- `/Users/agoodkind/Sites/tack/internal/audit/router.go`
- `/Users/agoodkind/Sites/tack/internal/audit/kafka_recorder.go`
- `/Users/agoodkind/Sites/tack/internal/audit/kafka_recorder_test.go`
- `/Users/agoodkind/Sites/tack/internal/audit/dual_read.go` (Wave A only)
- `/Users/agoodkind/Sites/tack/cmd/audit-consumer/main.go`
- `/Users/agoodkind/Sites/tack/internal/audit/consumer.go`
- `/Users/agoodkind/Sites/tack/internal/audit/consumer_test.go`
- `/Users/agoodkind/Sites/tack/internal/test/audit_kafka_e2e_test.go`
- `/Users/agoodkind/Sites/tack/migrations/005_audit_consumer_offsets.sql`
- `/Users/agoodkind/Sites/tack/migrations/006_audit_events_event_id_uniq.sql`
- `/Users/agoodkind/Sites/tack/migrations/007_audit_events_v2_sibling.sql` (Wave A only)
- `/Users/agoodkind/Sites/tack/scripts/audit-parity-kafka.sh`
- `/Users/agoodkind/Sites/tack/docs/audit-runbook-phase2.md`

### Phase 2, modified

- `/Users/agoodkind/Sites/tack/cmd/server/main.go` lines 207-250 and 316-347
- `/Users/agoodkind/Sites/tack/docker-compose.yml`: add `redpanda`, `audit-consumer`; remove `tack-audit-wal` volume in Wave B
- `/Users/agoodkind/Sites/tack/internal/config/config.go`: add Kafka env, deprecate `AUDIT_WAL_DIR`
- `/Users/agoodkind/Sites/tack/scripts/backup.sh`: add Redpanda backup with restore-test gate
- `/Users/agoodkind/Sites/tack/CLAUDE.md`: document the new topic, consumer offsets table, broker-vs-YB split

### Phase 2, deleted (Wave B cleanup)

- `/Users/agoodkind/Sites/tack/internal/audit/wal.go`
- `/Users/agoodkind/Sites/tack/internal/audit/wal_test.go`
- `/var/lib/tack/audit-wal/` on CT 117 (operator action after 7 clean days)

## 5. Risks and Unknowns

### 5a. Phase 1 risks

- **Idle-rotation races a slow appender.** If a producer holds `w.mu` for an unusually long write (slow disk, kernel hiccup), the drainer waits behind it. Result: a single-millisecond stall, no correctness impact. Tested by `TestRotationDuringHighThroughput`. Mitigation: `idleRotateAfter` is `2 * drainInterval` so transient lock contention does not trigger a forced rotation prematurely.
- **Backlog signal misfires on clock skew.** `oldestUnflushedUnixNano` is parsed from the filename which encodes the producer's clock. If the host clock jumps backward, an old segment may register as "in the future" and not trigger backlog age. Probability low (CT 117 runs ntp); mitigation: clamp the age to non-negative before comparing, treat negative as zero. Acceptable.
- **Backpressure timeout amplifies inbound HTTP timeouts.** A 250 ms wait on top of a slow MCP call could push the parent request past its deadline. Mitigation: the wait is also gated on `ctx.Done()`; the parent's HTTP timeout cancels the wait early. Backpressure can never extend the parent's deadline.
- **`atOverflow` going atomic loses one rare safety net.** Today's filesystem walk would catch a segment-leak class of bug (e.g., a segment with an unparseable name). The new code can miss that because it only updates atomics in `drainOnce`; if `drainOnce` itself fails, the atomics stay stale. Mitigation: `audit.wal.scan_failed` is already an Error log; the operator alert on it is unchanged. The drainer's last-success gauge is the second canary.
- **`MaxBacklogSegments=64` is wrong for very low traffic.** A site with hours-long idleness then a burst could see backpressure trip immediately if pre-existing segments accumulated. With idle rotation the segments DO drain, so empty-then-burst is exactly the case the rotation fix targets. Risk low, mitigated by rotation, surfaced by the rehearsal in 2i.

### 5b. Phase 2 risks

- **Broker death without backup.** Single-host single-broker means a disk failure on CT 117 loses every event between the broker write and the YB consumer commit. Mitigated only by short consumer lag (subsecond steady state) and the YB-side `audit.events` being authoritative. Severity: bounded loss in flight at moment of failure.
- **Empty-backup repeat.** The retro section 1A defect was a tarball that was never restore-tested. Phase 2 explicitly adds a nightly restore-into-fresh-container test for the Redpanda backup; if the test does not run, the same class of defect can recur. Mitigation: the backup script CI gate fails the deploy if the restore test was skipped.
- **Consumer lag during heavy load.** If the consumer cannot keep up, broker disk fills toward the 50 GiB retention bytes cap and oldest events get deleted before projection. Mitigation: `audit_kafka_lag_seconds` gauge plus paging alert at 60 s; retention is intentionally large enough to absorb several hours of unhandled lag. If we ever hit the cap, that is a sev-1 paging event.
- **Upgrade-induced producer error storms.** A broker restart will surface as `Recorder.Record` errors for read-class verbs. State-change verbs are unaffected because they bypass. Mitigation: schedule broker upgrades in maintenance windows; document in the runbook that read-class audit gaps during a 30-second broker bounce are expected.
- **Consumer offset table corruption.** `audit.consumer_offsets` is a YB row; if it is somehow advanced past unprocessed events the consumer skips them. Mitigation: the offset UPDATE is in the same YB transaction as the `audit.events` INSERT, so the offset can only be ahead of an existing audit row. A repair tool can replay from the last `audit.events.event_time` if the offsets table is lost.
- **Schema drift between producer and consumer.** Today's `Event` JSON payload is shared via the `audit` Go package; deploys are coordinated. If the consumer ships first and the producer ships a new field, the consumer ignores it (encoding/json default). If the producer ships first removing a field the consumer expects, the consumer reads zero-value. Standard JSON-schema-evolution risk. Mitigation: include a `schema_version` field in `Event`, refuse to produce when consumer's known version is older. Tracked as a Phase 2 detail.
