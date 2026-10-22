# Plan: cut the audit ledger over to Apache Kafka

Design of record for the audit Kafka cutover. The settled decisions below are
also recorded in `AGENTS.md`; if the two ever disagree, `AGENTS.md` wins.
Delivery is tracked on TACK-232.

## Context

Before the cutover every audit `Record()` call wrote synchronously into
`audit.events` in YugabyteDB through `internal/audit/yugabyte.go`. The cutover
moves the producer onto Apache Kafka: `Record()` publishes to a Kafka topic, the
`audit-consumer` service reads the topic, and the consumer becomes the only
writer of `audit.events` and `audit.chain_heads`. That single-writer property
also closes the concurrent-write race in TACK-271 (the audit chain-race ticket).

A superseded earlier design used a dual-write period and a sibling table
`audit.events_v2`. That is abandoned. The canonical table stays `audit.events`,
and there is no `events_v2`.

## Settled decisions

- **One table.** Keep the single `audit.events` table. The consumer continues
  the existing per-`(org, shard)` hash chain through `audit.chain_heads`. No
  rename, no archive table, no `events_v2`.
- **One migration.** A single `003_audit_kafka_cutover.sql` replaces the existing
  `003`, `004`, and `005`, which are deleted. Production is at migration `002` and
  migrates straight to `003`. QA is recreated from empty. Both are clean because
  no live database has applied `003`, `004`, or `005`.
- **Multiple consumers are supported.** See the multi-consumer section below.
- ClickHouse is optional. Canonical recording, ledger queries, and deployment
  operate without it.

## The single migration: `003_audit_kafka_cutover.sql`

Delete `003_audit_kafka_consumer.sql`, `004_audit_events_event_id_uniq.sql`, and
`005_audit_events_v2_sibling.sql`. Write one new `003` that contains:

- the `audit.consumer_offsets` table (was the old `003`),
- the unique index on `audit.events (event_id, event_time)` (was the old `004`;
  the index must include `event_time` because `audit.events` is partitioned by
  it),
- the new `audit.events_dlq` dead-letter table.

It leaves `audit.events` in place and never creates `audit.events_v2`.

## Code changes

- `internal/audit/consumer.go`: write `audit.events` and `audit.events_dlq`,
  advance `audit.chain_heads` as the only writer, and reuse the producer's
  `event_id` from the message instead of generating a new one.
- Delete `internal/audit/dual.go`. `cmd/server/main.go` uses `KafkaRecorder` when
  `AUDIT_KAFKA_BROKERS` is set, otherwise `YBRecorder`. The WAL path is removed
  with the dual path.
- Remove the `events` versus `events_v2` parity tooling
  (`internal/ops/audit_parity*.go`).
- `seed-audit-roles` is done: `./server ops audit seed-roles` creates the LOGIN
  roles `tack_audit_writer`, `tack_audit_reader`, `tack_audit_redactor` (the
  names the configs DSNs use) and replaced the old shell script. This is
  TACK-295 (the audit-login-role ticket).

## Multi-consumer chain safety

The chain is per `(org, shard)`. To let multiple consumers run safely, key each
Kafka message by the chain unit:

- `internal/audit/recorder.go` adds an `EventID` field to `Event`, set at the
  recording call site.
- `internal/audit/kafka_recorder.go`: `Record` computes `shard` from
  `(actor_id, event_id)`, keys each message by `(org_id, shard)`, and stamps
  `event_id` on the message.

Each `(org, shard)` chain then lives on one Kafka partition, so exactly one
consumer writes it and no chain-head lock is needed. The stable `event_id` also
makes the `(event_id, event_time)` unique index give real idempotency across
redelivery.

## Optional ClickHouse projection

YugabyteDB stores the canonical ledger and serves the operator audit commands.
The application does not query ClickHouse. The consumer can optionally copy
committed events into `audit.events_olap` and repair that copy after an outage.

The default audit deployment does not start ClickHouse, configure its DSN, or
mount its consumer secret. Explicit opt-in requires the `clickhouse` profile,
the [Compose overlay](../../docker-compose.clickhouse.yml), and protected
`CLICKHOUSE_PASSWORD` and `AUDIT_CONSUMER_CLICKHOUSE_DSN` secret files. Only
that overlay makes the consumer wait for ClickHouse health. The optional copy
does not replace the canonical ledger.

## Pre-cutover work

- Configs renders `KAFKA_CLUSTER_ID`, `AUDIT_KAFKA_BROKERS`, and
  `COMPOSE_PROFILES` in `tack.env.j2`.
- The `kafka` and `audit-consumer` compose services carry
  `profiles: [audit]` and stay off until the audit profile is enabled.

## Cutover, QA first

QA is disposable, so prove the whole thing there before prod.

1. Build a branch image and deploy QA pinned to that SHA with the Ansible
   `deploy-tack` playbook (`--limit tack_qa_servers`). This renders `.env`,
   fetches the stack, and brings up the services.
2. Run `./server migrate` (applies the single `003`), then
   `./server ops audit seed-roles`, then restart `app` and `audit-consumer`.
3. Smoke test: a handful of MCP read and write calls each land one row in
   `audit.events`, written by the consumer; `audit.chain_heads` advances by one
   per write; no rows land in `audit.events_dlq`. Run these checks without
   ClickHouse or its secret files. Run two `audit-consumer` replicas and confirm
   each `(org, shard)` chain stays single-writer.

## Cutover, production

1. Take a fresh backup and confirm the restore drill passes
   (`./server ops backup ...`; see `docs/runbooks/recovery.md`).
2. Bring up `kafka` and `audit-consumer`, and confirm the consumer connects to
   Kafka and YugabyteDB without ClickHouse.
3. Stop `app` (MCP goes unavailable here), run `./server migrate` (002 to 003),
   run `./server ops audit seed-roles`, deploy the new app image with
   `./server ops deploy`, and start `app`.
4. Run the same smoke test as QA.

## Rollback

If the smoke test fails, redeploy the prior app image and stop the audit-profile
services; they idle harmlessly when the producer is not publishing. The single
`003` only adds tables and an index and leaves `audit.events` in place, so there
is no table rename to reverse.

## Open question to confirm during implementation

Confirm the producer waits for a durable Kafka write (`acks=all`, synchronous
produce) before `Record()` returns, so an MCP call cannot report success before
its audit event is durable. The current `KafkaRecorder` uses `ProduceSync` with
`AllISRAcks`, which satisfies this; verify it stays that way.

## Out of scope

Horizontal scale beyond a single consumer replica at first cutover, and cold
archive of old `audit.events` rows to object storage, are later stages.
