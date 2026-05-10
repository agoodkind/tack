# Wave 1 Producer Implementation Report

## 1. Worktree path and branch

- Worktree path: `/Users/agoodkind/Sites/tack`
- Branch: `main` (5 commits ahead of `origin/main`)

The prompt described an "isolated worktree" but the assigned working tree
was the primary `main` checkout. All changes are unstaged or untracked
(see section 2).

## 2. Files created and modified

### Created

- `internal/audit/kafka_recorder.go` (190 lines): `KafkaRecorder`,
  `KafkaConfig`, `MarshalEvent`, `SplitBrokers`, plus the `kafkaPartitionKey`
  helper. Synchronous franz-go producer wired with `acks=all`,
  `lz4` batch compression, 5 ms linger, default produce-request timeout
  matching the configured produce timeout.
- `internal/audit/kafka_recorder_test.go` (211 lines): kfake-backed unit
  tests (see section 3).
- `internal/audit/dual.go` (92 lines): `DualRecorder` with primary-first,
  secondary-second produce order. Errors are joined via `errors.Join` and
  every failure path emits a `slog.ErrorContext` line before returning.
- `internal/audit/dual_test.go` (163 lines): in-memory and observing
  recorder fakes plus the four required behaviors.

### Modified

- `cmd/server/main.go`: split `buildAuditRecorder` into helper functions;
  added `wrapAuditWithKafka` that returns a `DualRecorder(KafkaRecorder,
  WALRecorder)` when `AUDIT_KAFKA_BROKERS` is set, else the unchanged WAL
  recorder.
- `internal/config/config.go`: added `AuditKafkaBrokers`,
  `AuditKafkaTopic`, `AuditKafkaClientID`, `AuditKafkaProduceTimeout`
  fields with the design-doc defaults (`audit.events.v1`,
  `tack-audit-producer`, `10s`).
- `docker-compose.yml`: added `kafka` (Apache Kafka 4.2.0, KRaft combined
  broker+controller, 256 partitions, RF=1, MIS-R=1) and `seaweedfs`
  (chrislusf/seaweedfs:3.71, master+volume server, internal-only). Added
  matching `kafka-data` and `seaweedfs-data` named volumes. Wired the
  four `AUDIT_KAFKA_*` env vars into the `app` service block.
- `.env.example`: documented `KAFKA_CLUSTER_ID` (with the
  `kafka-storage.sh random-uuid` generation steps in a comment) and the
  four `AUDIT_KAFKA_*` knobs with default values for ergonomic copy.
- `go.mod`/`go.sum`: added `github.com/twmb/franz-go v1.21.1` and
  `github.com/twmb/franz-go/pkg/kfake` (test-only).

## 3. Tests added

### `kafka_recorder_test.go`

- `TestKafkaRecorderProducesAndCommits`: produces 100 events through a
  one-broker `kfake.Cluster`, then opens a fresh consumer client to drain
  the topic and assert all 100 messages are delivered.
- `TestKafkaRecorderReturnsErrorOnBrokerDown`: builds a recorder against
  a live `kfake.Cluster`, calls `cluster.Close()`, then asserts that the
  next `Record` call returns a non-nil error within the produce timeout.
- `TestKafkaRecorderPayloadShape`: produces one event, fetches the
  matching record from the topic, unmarshals the value into `Event`, and
  asserts every interesting field round-trips (verb, actor.id, entity.id,
  context.org_id, context.tool, occurred_at, outcome).
- `TestKafkaRecorderRequiresBrokers` and `TestSplitBrokers`: cover the
  config validation and broker-list parsing helpers.

### `dual_test.go`

- `TestDualWritesToBoth`: two `MemoryRecorder`s, asserts both received
  the event after one `DualRecorder.Record`.
- `TestDualReturnsFirstError`: primary returns a sentinel error,
  secondary is a `MemoryRecorder`. Asserts the returned error wraps the
  sentinel via `errors.Is`, AND that the secondary still received the
  event.
- `TestDualReturnsBothErrors`: both sides fail with distinct sentinels;
  asserts the returned error wraps both via `errors.Is`.
- `TestDualPropagatesContextCancellation`: both sides observe a
  cancelled `ctx.Err()` and the `errors.Is(err, context.Canceled)`
  assertion holds.
- `TestDualNilRecorderRejected`: validation gate.

## 4. Compose services added

| Service     | Image                       | Internal port  | Volume            |
|-------------|-----------------------------|----------------|-------------------|
| `kafka`     | `apache/kafka:4.2.0`        | 9092 PLAINTEXT | `kafka-data`      |
| `seaweedfs` | `chrislusf/seaweedfs:3.71`  | none exposed   | `seaweedfs-data`  |

Notes:

- Both services live on the existing IPv6-only `default` network; no
  external port is exposed for either, matching the design constraint.
- Kafka runs combined `broker,controller` with `KAFKA_NODE_ID=1`, the
  CLUSTER_ID injected via `${KAFKA_CLUSTER_ID:?...}` from `.env`. The
  generation procedure is in a comment inside the service block.
- Listener config: `PLAINTEXT://[::]:9092,CONTROLLER://[::]:9093` so both
  the data plane and the controller plane bind on the IPv6 stack.
- 256 partitions, RF=1, MIS-R=1, `unclean.leader.election.enable=false`.

## 5. Env vars added

| Variable                       | Default                  | Notes |
|--------------------------------|--------------------------|-------|
| `AUDIT_KAFKA_BROKERS`          | empty (Kafka disabled)   | Comma-separated bootstrap broker list. |
| `AUDIT_KAFKA_TOPIC`            | `audit.events.v1`        | Per design doc section 12.1. |
| `AUDIT_KAFKA_CLIENT_ID`        | `tack-audit-producer`    | Producer client ID for broker logs. |
| `AUDIT_KAFKA_PRODUCE_TIMEOUT`  | `10s`                    | Caps a single `Record` call. |
| `KAFKA_CLUSTER_ID`             | empty (compose fails)    | Pinned KRaft cluster ID. Generation procedure documented in compose. |

## 6. Verification results

- `make test`: PASS for every package; the audit package took ~48s due
  to existing WAL/notarizer tests, and all five new dual tests plus all
  five new kafka tests pass.
- `make build`: FAILS, but only on consumer-worktree-owned files
  (`cmd/audit-consumer/main.go`, `internal/audit/consumer.go`,
  `internal/audit/consumer_test.go`). See section 7.
- `gofmt -l` on the producer-side files (`internal/audit/kafka_recorder.go`,
  `internal/audit/kafka_recorder_test.go`, `internal/audit/dual.go`,
  `internal/audit/dual_test.go`, `cmd/server/main.go`,
  `internal/config/config.go`): empty.
- `make lint-files LINT_FILES="..."` scoped to the producer-side files:
  `golangci-lint: OK (0 findings)`, `staticcheck-extra: OK (0 new
  findings)`.
- `grep -nP '[\x{2014}\x{2013}]'` over the listed files (including
  `.env.example` and `docker-compose.yml`): empty.

## 7. Deviations from the prompt

- The "isolated worktree" was actually the primary `main` working tree.
  Several artifacts from the parallel consumer worktree were already
  present here: `internal/audit/consumer.go`, `internal/audit/consumer_test.go`,
  `cmd/audit-consumer/`, `migrations/003_audit_kafka_consumer.sql`,
  `migrations/004_audit_events_event_id_uniq.sql`,
  `migrations/005_audit_events_v2_sibling.sql`,
  `internal/config/config.go`'s `AuditConsumer*` fields, and
  `scripts/backup-*.sh`. The prompt forbids me from touching any of
  those files. They were left as-is.
- Because those consumer files exist in the same Go package
  (`internal/audit`), the package-level `make test` and `make build` are
  affected by their lint state and (initially) their import errors.
  Tests pass once the dependencies they pull in (`franz-go`,
  `clickhouse-go/v2`) are resolved. Lint reports two findings inside
  consumer-owned files plus a `gofmt` formatting issue in
  `consumer_test.go`. None are in producer-side files. The producer-side
  build target compiles cleanly.
- A `for i := 0; i < 8; i++` loop in `internal/audit/wal.go:265` shows
  up as a `golangci-lint` `intrange` finding under `make build`. It is
  in the pre-existing modified `wal.go`, which the prompt forbids me
  from touching.
- The agent-gate hook blocks new `//nolint` directives and direct `go`
  invocations. To stay within the rule set, `KafkaRecorder.Record` does
  NOT default `OccurredAt` at the producer layer (the rule against
  `time.Now()` outside a clock helper would fire). The Wave 1 design
  expects callers to populate `OccurredAt` upstream, and the existing
  WAL/Yugabyte/Memory recorders all set it on entry, so the producer
  takes it verbatim. This is documented in the `Record` doc comment and
  is necessary for the Wave 1 parity gate (otherwise the Kafka leg and
  WAL leg of `DualRecorder` would carry different timestamps).
- `KafkaRecorder.Close()` is parameterless to match the
  `interface{ Close() error }` shape that `DualRecorder.Close` already
  type-asserts against. A sibling `CloseContext(ctx)` was added for
  callers that want to thread a context (the dual-setup error path in
  `cmd/server/main.go` uses it to satisfy `contextcheck`).

