# Plan: cut audit ledger over to Apache Kafka in a single hard cutover

## Context

The audit subsystem writes every audit event directly into YugabyteDB today through `internal/audit/yugabyte.go`. The agreed-upon next step is to move that write path onto Apache Kafka so the audit ledger can scale horizontally (multiple consumers, multiple brokers) without the producer waiting on a database write. The migration happens in one hard cutover with a brief maintenance window, not as a parallel dual-write period.

A previous design (committed at `a5aec6d`) introduced dual-write code that fanned every event to both YugabyteDB and Kafka, accumulated rows in a sibling table called `audit.events_v2`, and would have validated parity before cutting over. That design is now superseded. Tack has no external clients yet, so a brief audit-availability gap during a rename is acceptable, and the dual-write machinery is unnecessary complexity. The cutover plan below replaces it.

## What changes

Before the cutover, every audit `Record()` call writes synchronously into `audit.events` in YugabyteDB. After the cutover, every `Record()` call publishes the event to Apache Kafka, the `audit-consumer` service reads the Kafka topic, and the consumer is the only writer of `audit.events`. The hash chain in `audit.chain_heads` is still maintained, but the consumer becomes the single writer of those rows. That single-writer property also resolves the concurrent-Record race documented in TACK-271, because two concurrent producer calls no longer race on the chain head.

## Pre-cutover work

The Compose stack needs an Apache Kafka service running on the production host. The Compose definitions already exist in the working tree from the earlier wave 1 commit. The `kafka` service runs in KRaft mode without ZooKeeper. The `audit-consumer` service runs the standalone Go binary at `cmd/audit-consumer/`. Both must be running and healthy before the cutover begins.

The `audit-consumer` projection target must be `audit.events` rather than `audit.events_v2`, which is what the wave 1 code currently writes. That requires an edit in `internal/audit/consumer.go` so the consumer writes into the canonical table. Migration `005_audit_events_v2_sibling.sql` should not run against production; the sibling table is no longer part of the plan.

Recommended migration shape:

- `003_audit_kafka_consumer.sql` (consumer offsets table) ships as-is. The consumer needs it to track its position in the Kafka log.
- `004_audit_events_event_id_uniq.sql` is already in production and stays.
- `005_audit_events_v2_sibling.sql` gets removed from the migration set before deploy.

## Code changes before cutover

The dual-write fan-out at `internal/audit/dual.go` is no longer needed and gets removed. The Kafka producer at `internal/audit/kafka_recorder.go` becomes the primary recorder rather than a sibling of the YugabyteDB recorder. The wire-up at `cmd/server/main.go` switches from "wrap YBRecorder in a DualRecorder" to "use KafkaRecorder when `AUDIT_KAFKA_BROKERS` is set, fall back to YBRecorder otherwise." The fallback keeps the existing deploy path working for environments where Kafka is not yet stood up (development and tests).

The consumer at `internal/audit/consumer.go` writes into `audit.events`, advances `audit.chain_heads` as the single writer, and writes failed events into a dead-letter table called `audit.events_dlq`. The dead-letter table replaces the wave 1 sibling table; one fresh migration creates it.

State-change verbs and read-class verbs both go through Kafka after cutover. The earlier design treated them differently (state-change verbs bypassed the WAL); with Kafka as the single producer-side destination, the WAL goes away and both verb classes follow the same path. The compliance contract is preserved because the Kafka append is synchronous from the producer's perspective and the consumer extends the chain in a single transaction with the row insert.

## Cutover steps

The cutover runs against production on the operator's authority. The expected window is a few seconds to a couple of minutes of MCP unavailability.

1. Take a fresh backup using `make backup` and confirm `make backup-verify` passes.
2. Bring the new Kafka service up alongside the running app: `ssh tack 'cd /root/tack && docker compose up -d kafka audit-consumer'`. Wait for both to be healthy.
3. Confirm the consumer can connect to YugabyteDB and Kafka by checking its startup logs: `ssh tack 'docker compose logs --tail 50 audit-consumer'`.
4. Stop the app: `ssh tack 'cd /root/tack && docker compose stop app'`. MCP becomes unavailable from this point.
5. Run the rename migration. This renames `audit.events` to `audit.events_legacy_archive` and creates a fresh empty `audit.events` partitioned the same way the original was, plus the `audit.events_dlq` table. Migration file: a new `006_audit_kafka_cutover.sql` to be written.
6. Deploy the new app image that includes the Kafka-recorder wiring. Build with `make build`; ship and load via the existing `make deploy` path (or `./server ops deploy` once TACK-233 lands).
7. Start the new app: `ssh tack 'cd /root/tack && docker compose start app'`. MCP becomes available again.
8. Run a smoke test: five MCP calls covering read and write verbs (describe_workspace, list_projects, get_project, create_issue, get_issue). Confirm five rows land in `audit.events` via a SELECT.
9. Confirm `audit.chain_heads` advanced as expected and the consumer wrote it (not the producer).

## Rollback

If the smoke test fails for any reason, the rollback runs the rename migration in reverse: drop the new empty `audit.events`, rename `audit.events_legacy_archive` back to `audit.events`, redeploy the prior app image. The Kafka and audit-consumer services can be left running; they idle harmlessly when the producer is not publishing.

The pre-cutover backup at step 1 is the heavy-duty safety net if the rename and rollback path itself misbehaves.

## Smoke test details

The five MCP calls are the same set the `./server ops audit smoke` subcommand should issue once it exists. The test asserts:

- Each call produces the expected verb in `audit.events` within 5 seconds.
- The `prev_hash` of each new row matches the `row_hash` of the previous row in `audit.chain_heads`.
- The `chain_heads.last_seq` advances by one per write.
- No rows land in `audit.events_dlq`.

## Open questions

- The producer-side acknowledgement semantics. `KafkaRecorder.Record()` needs to wait for the Kafka write to be durable before returning, or the calling MCP request might return success before the audit event is recorded. Confirm the wave 1 implementation uses `acks=all` and a synchronous produce path.
- The hash-chain race in TACK-271 closes naturally once the consumer is the only chain writer, but only if the consumer is also serialized per `(org_id, shard)`. Confirm the consumer processes events for the same shard in order. If it runs partitions in parallel, an explicit `SELECT FOR UPDATE` on the chain head row stays necessary.
- The Compose `audit-consumer` service today depends on Kafka, YugabyteDB, and ClickHouse all being healthy. ClickHouse is for an OLAP projection that comes in a later stage. The cutover should not require ClickHouse to be running.
- The `AUDIT_KAFKA_*` environment variables that the producer and consumer both need are not yet present in the production `.env`. Adding them is part of the pre-cutover work.

## Out of scope

Horizontal scale of audit consumers (multiple consumer replicas, partition rebalancing) is a later stage. Today's plan is the cutover only. The post-cutover system runs a single consumer replica, which is operationally identical to the current single-writer YBRecorder shape.

Cold archive of older `audit.events` rows to object storage is also a later stage, tracked separately. Today's plan keeps everything hot in Yugabyte.

## Verification

After the cutover the system is healthy when:

- `docker compose ps` shows app, kafka, audit-consumer all up and healthy.
- The smoke-test MCP calls produce expected rows in `audit.events`.
- The notarizer continues signing Merkle roots every minute.
- No rows land in `audit.events_dlq` during normal operation.
- `audit.chain_heads` advances on every write and is sourced from the consumer's update transaction, not the producer.
