# Cut the audit ledger over to Apache Kafka

TACK-232 tracks this cutover. Follow `AGENTS.md` if its binding requirements
conflict with this plan.

Before cutover, `Record()` synchronously wrote `audit.events` in YugabyteDB
through `internal/audit/yugabyte.go`. After cutover, `Record()` publishes to
Kafka and audit-consumer alone writes `audit.events` and `audit.chain_heads`.
A single writer per chain also resolves the concurrent-write race in TACK-271.

## Preserve the ledger and consolidate migration

- Keep `audit.events` and continue each `(org, shard)` chain from
  `audit.chain_heads`. Do not rename the table, add an archive table, or create
  the superseded `audit.events_v2` sibling table.
- Replace `003_audit_kafka_consumer.sql`,
  `004_audit_events_event_id_uniq.sql`, and `005_audit_events_v2_sibling.sql`
  with `003_audit_kafka_cutover.sql`. At this cutover, production has applied
  `002` and neither live database has applied the replaced migrations.
  Upgrade production directly to `003` and recreate disposable QA from empty.
- Support multiple consumers with partitioned chain ownership.

The consolidated migration creates `audit.consumer_offsets`, the unique index
on `audit.events (event_id, event_time)`, and dead-letter table
`audit.events_dlq`. Include `event_time` in the unique index because the table
partitions by that column. Preserve `audit.events`.

## Update recording and consumption

Make `internal/audit/consumer.go` write events and dead letters, advance chain
heads as the sole writer, and reuse the producer's `event_id`.

Delete `internal/audit/dual.go` and its WAL recording implementation. Select
`KafkaRecorder` in `cmd/server/main.go` when `AUDIT_KAFKA_BROKERS` has a value;
otherwise select `YBRecorder`. Delete parity tooling in
`internal/ops/audit_parity*.go` for `events` and `events_v2`.

TACK-295 already replaced the shell role-seeding script with
`./server ops audit seed-roles`. It creates the LOGIN roles
`tack_audit_writer`, `tack_audit_reader`, and `tack_audit_redactor` that configs
uses in its DSNs.

## Assign each chain to one consumer

Add `EventID` to `Event` in `internal/audit/recorder.go` and assign it at each
recording call. In `internal/audit/kafka_recorder.go`, derive the shard from
`(actor_id, event_id)`, key the message by `(org_id, shard)`, and include the
same `event_id` in its payload.

Each chain's messages use one Kafka partition. One consumer writes that
partition without a chain-head lock. The unique `(event_id, event_time)` index
prevents duplicate rows after redelivery.

## Prepare configuration

Render `KAFKA_CLUSTER_ID`, `AUDIT_KAFKA_BROKERS`, and `COMPOSE_PROFILES` in
configs `tack.env.j2`. Set `profiles: [audit]` on the `kafka` and
`audit-consumer` services. Start them only when configuration enables that profile.

## Validate on QA

1. Build a branch image and deploy its exact SHA with Ansible `deploy-tack`,
   limited to `tack_qa_servers`. The deploy renders `.env`, fetches the stack,
   and starts services.
2. Run `./server migrate` to apply the consolidated `003`, then
   `./server ops audit seed-roles`. Restart `app` and `audit-consumer`.
3. Make several MCP read and write calls. Require one `audit.events` row per
   call, one chain-head advance per ledger write, and an empty
   `audit.events_dlq`. Run two consumer replicas and verify that each
   `(org, shard)` chain still has one writer.

## Cut over production

1. Take a fresh backup and require a passing restore drill under the
   [recovery procedure](../runbooks/recovery.md), using `./server ops backup ...`.
2. Start `kafka` and `audit-consumer` and verify consumer connections to Kafka
   and YugabyteDB.
3. Stop `app`, making MCP unavailable. Run `./server migrate` from `002` to
   `003`, then `./server ops audit seed-roles`. Deploy the new app image with
   `./server ops deploy` and start `app`.
4. Repeat QA's MCP and chain checks.

## Roll back

If verification fails, redeploy the prior app image and stop audit-profile
services. The consumer remains idle when the producer does not publish.
Migration `003` only adds tables and an index; preserve it and the canonical
ledger table rather than attempting a table rename reversal.

## Verify durable publication and limit scope

Require synchronous Kafka publication with `acks=all` before `Record()` returns.
An MCP call must not report success before its event becomes durable. Verify
that `KafkaRecorder` retains `ProduceSync` with `AllISRAcks` during implementation.

The first production cutover uses one consumer replica. The two-replica QA check
validates concurrent chain ownership before that cutover. Later stages add
operational scale and cold archival of old ledger rows to object storage.
