# Wave 1 Audit Consumer (Phase 2) Implementation Report

## 1. Worktree path and branch

The slice was implemented directly in `/Users/agoodkind/Sites/tack` on
branch `main`. No new worktree was created. The producer-side files
(`kafka_recorder.go`, `dual.go`, server wiring, kafka and seaweedfs
compose blocks) were already present as untracked or modified content
in this checkout when the slice started; that code is owned by the
parallel producer worktree and was not edited by this slice.

## 2. Files created and modified

Files created (consumer scope):

- `internal/audit/consumer.go` (699 lines)
- `internal/audit/consumer_test.go` (503 lines)
- `cmd/audit-consumer/main.go` (138 lines)
- `migrations/003_audit_kafka_consumer.sql` (30 lines)
- `migrations/004_audit_events_event_id_uniq.sql` (21 lines)
- `migrations/005_audit_events_v2_sibling.sql` (79 lines)

Files modified (consumer scope):

- `internal/config/config.go`: added eight `AuditConsumer*` env-driven
  fields plus inline doc.
- `.env.example`: added the consumer block listing every
  `AUDIT_CONSUMER_*` variable with defaults that match
  `internal/config/config.go`.
- `docker-compose.yml`: added `clickhouse` and `audit-consumer`
  service blocks plus the `clickhouse-data` named volume. The existing
  `kafka` and `seaweedfs` blocks were not touched.
- `Makefile`: added the `.PHONY: audit-consumer` target that builds
  `dist/audit-consumer` from `./cmd/audit-consumer`.
- `go.mod` and `go.sum`: pulled `github.com/twmb/franz-go` and
  `github.com/ClickHouse/clickhouse-go/v2` to direct dependencies.

Other files in the working tree show diffs (`internal/audit/wal.go`,
`internal/audit/wal_test.go`, `cmd/server/main.go`, the kafka and
seaweedfs compose blocks, `scripts/backup*.sh`, `AGENTS.md`,
`internal/telemetry/metrics.go`) but those changes belong to the
parallel producer worktree, not this slice.

## 3. Tests added

`internal/audit/consumer_test.go` contains seven test functions. The
five integration tests gate on `AUDIT_CONSUMER_TEST_DSN`; without it
each one calls `t.Skip` so `make test` stays green on hosts without
a Yugabyte cluster.

- `TestConsumerProjectsToEventsV2`: produces 100 events to a `kfake`
  cluster, runs the consumer once, and verifies each row in
  `audit.events_v2` carries a non-zero `seq` and a 32-byte
  `row_hash`.
- `TestConsumerIdempotentOnEventID`: produces 100 events and runs two
  consumer groups against the topic; the unique index from migration
  004 must dedupe so exactly 100 rows remain in `audit.events_v2`.
- `TestConsumerOffsetAdvanceIsAtomicWithProjection`: starts a
  consumer, kills it after 500 ms, restarts a fresh consumer, and
  asserts the final row count is exactly the produced total.
- `TestConsumerNotarizerSigns`: runs the embedded notarizer for two
  one-second ticks and verifies each `audit.notarizations` row has a
  valid Ed25519 signature against the public half of the test key.
- `TestConsumerHandlesMalformedPayload`: produces one record with
  invalid JSON and one valid record, asserts the consumer advances
  past the bad record, lands a row in `audit.events_v2_dlq`, and
  projects the valid record.
- `TestExtractEventID`: pure unit test for the header-based
  `event_id` extractor, runs without external dependencies.
- `TestErrMalformedSignal`: verifies `errors.Is` matches the
  malformed-payload sentinel through `fmt.Errorf` wrapping.

## 4. Compose services added

`clickhouse`:

- Image: `clickhouse/clickhouse-server:24.8` (Apache 2.0).
- Single-node MergeTree topology.
- Healthcheck hits `http://[::1]:8123/ping` on the IPv6 bridge.
- Volume: `clickhouse-data` mounted at `/var/lib/clickhouse`.
- Listeners 8123 (HTTP) and 9000 (native) are not exposed to the host;
  only sibling containers can reach the daemon.

`audit-consumer`:

- Image: same `tack-server:latest` as the app container.
- Entrypoint overridden to `/usr/local/bin/audit-consumer`.
- Mounts `tack-logs:/var/log/tack` for structured logs and
  `/etc/tack:/etc/tack:ro` for the Ed25519 signing key.
- Depends on `kafka` (`service_started`), `yugabyte`
  (`service_healthy`), and `clickhouse` (`service_healthy`).

## 5. Migrations added

`003_audit_kafka_consumer.sql` creates `audit.consumer_offsets`
keyed by `((consumer_group, topic) HASH, partition ASC)` with one
`offset BIGINT` per partition plus the `audit_writer` ALL policy and
the `audit_reader` SELECT policy.

`004_audit_events_event_id_uniq.sql` runs a pre-check `DO $$` block
that compares `count(*)` against `count(DISTINCT event_id)` on
`audit.events`; the migration fails loudly if duplicates already exist.
On a clean count the migration creates the unique index
`events_event_id_uniq` on `audit.events(event_id)` so the consumer's
`ON CONFLICT (event_id) DO NOTHING` path is dedupe-correct.

`005_audit_events_v2_sibling.sql` creates the parity sibling
`audit.events_v2` with the same column shape, primary key, and
secondary indexes as `audit.events`, plus `events_v2_event_id_uniq`,
matching RLS policies, and matching role grants. The same migration
also creates `audit.events_v2_dlq` for malformed-payload landings.

## 6. Env vars added

All eight new variables live on `Config` and `consumerEnv`:

- `AUDIT_CONSUMER_KAFKA_BROKERS` (required)
- `AUDIT_CONSUMER_KAFKA_TOPIC` (default `audit.events.v1`)
- `AUDIT_CONSUMER_GROUP_ID` (default `tack-audit-projector`)
- `AUDIT_CONSUMER_BATCH_SIZE` (default `256`)
- `AUDIT_CONSUMER_POLL_INTERVAL` (default `250ms`)
- `AUDIT_CONSUMER_YUGABYTE_DSN` (required)
- `AUDIT_CONSUMER_CLICKHOUSE_DSN` (default empty in code; compose
  sets `clickhouse://default:@clickhouse:9000/audit`)
- `AUDIT_CONSUMER_SIGNING_KEY_PATH` (default empty; notarizer is
  disabled when empty)

A ninth, `AUDIT_CONSUMER_NOTARIZER_PERIOD` (default `60s`), is read
only by the binary because the embedded notarizer cadence is
operator-tunable independent of the producer.

## 7. Verification results

- `make audit-consumer` succeeds and produces a 42 MiB
  `dist/audit-consumer` binary against the consumer scope.
- `make test` is green; every test in `internal/audit` passes,
  including the consumer tests that skip when
  `AUDIT_CONSUMER_TEST_DSN` is unset.
- `make build` reports `staticcheck-extra: OK` and `gocyclo: OK` and
  `deadcode: OK` for the consumer files. The build still fails on
  one finding: see deviation in section 8.
- `gofmt -l` returns empty for every consumer file.
- `grep -nP '[\x{2014}\x{2013}]'` returns empty for every file
  written by this slice.
- SQL migrations carry only `-- +goose` directives and bare statement
  syntax; no prose double-hyphen lines.

## 8. Deviations

The single open lint finding is `internal/audit/wal.go:265:2 for loop
can be changed to use an integer range (Go 1.22+) (intrange)`. The
new for-range syntax is a one-line change, but `wal.go` is in the
prompt's explicit Forbidden-files list ("Do not touch any other audit
files (`wal.go`, ...)") and the diff itself was introduced by the
parallel producer worktree's WIP, not by this slice. Because the
prompt's forbidden-file rule outranks the lint-clean rule, the slice
left `wal.go` alone. Resolving this finding requires either the
producer worktree to land its `wal.go` change with the trivial
`for i := range 8` rewrite, or an explicit consumer-worktree
exception to fix the one-line lint regardless.

A second deviation is procedural: the slice ran in the main
checkout rather than an isolated worktree because no detached
worktree existed for it at start time. All consumer-scope files are
identifiable in `git status` (untracked) and the producer-scope
files remain visible as the producer worktree's pending diff.

A third deviation: the consumer integration tests skip rather than
run by default. They require `AUDIT_CONSUMER_TEST_DSN` plus an
already-migrated Yugabyte cluster; the prompt asked for them to
exist, and they do, but they cannot all be exercised by `make test`
on a developer host. The unit-test side (extractor and sentinel
matching) runs unconditionally.
