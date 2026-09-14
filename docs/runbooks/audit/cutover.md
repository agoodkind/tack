# Prod audit cutover runbook

Status: **executed on prod 2026-06-09, succeeded.** Run via
`deploy deploy-tack --limit tack_servers` at SHA `634562b` (which pins YugabyteDB
at 2024.2.8.0-b85 so the cutover changes only the audit pipeline, not the engine;
see TACK-330). Verified end to end: the audit-consumer self-provisioned, became
the single writer, ensured `audit.events.v1` at 256 partitions, the notarizer
signs per-(org, shard) Merkle roots, and a live MCP call produced new chained
`audit.events` rows. No rollback was needed. The deploy reported a non-zero exit
only from the trailing `sysctl --system` handler failing on host-only `kernel.*`
keys in the unprivileged LXC, since fixed in configs (the handler now runs
`sysctl -p /etc/sysctl.d/99-tack-ipv6.conf`). The earlier QA validation
(2026-06-07, all eight audit acceptance behaviors) still holds. The durable
replacement for this manual sequence is the provision entrypoint tracked in epic
TACK-18 / TACK-303 ("deploy is not provision").

This runbook hardcodes no values. Every name, tag, count, and secret is read from
the source of truth named inline. Do not paste literal SHAs, passwords, hostnames,
DSNs, or partition counts into commands; resolve them from the sources below.

## What this changes

Prod currently runs no audit profile (`COMPOSE_PROFILES` has no `audit`, and prod
is on its pre-cutover migration). This cutover turns on the audit profile (kafka,
clickhouse, audit-consumer) for the first time. Prod is already provisioned for
FoundationDB and product data, so the FDB `configure new` and product `seed`
first-boot steps do NOT apply here. Only the audit first-boot items do.

## Sources of truth (resolve values from these, do not hardcode)

- Deploy ref: the commit/image SHA you intend to ship. It must be a SHA whose
  `build-push` CI run is green (the `overlay-drift` gate plus both image builds).
- Stack and env: `docker-compose.yml` plus the configs render of
  `tack/tack.env.j2` and the prod host group vars (audit profile, the per-host
  `KAFKA_CLUSTER_ID`, audit role passwords, and the audit DSNs). The rendered
  `.env` on the host is the effective truth.
- Kafka topic name: the rendered `.env` `AUDIT_KAFKA_TOPIC` (producer) and
  `AUDIT_CONSUMER_KAFKA_TOPIC` (consumer). They must match.
- Kafka brokers: the rendered `.env` `AUDIT_KAFKA_BROKERS`.
- Topic partition count: the audit shard space. The shard is `shardOf()` in
  `internal/audit/canonical.go` (currently the low byte of a CRC32, so 256
  shards). Create the topic with one partition per shard. Treat the count as
  "whatever the shard width is", not a magic 256; if the shard width changes
  (TACK-306), match it. Replication factor: the broker count for that environment
  (single-broker host means 1).
- Audit role passwords: the rendered `.env` `AUDIT_WRITER_PASSWORD`,
  `AUDIT_READER_PASSWORD`, `AUDIT_REDACTOR_PASSWORD` (vault-sourced). Never echo
  them.

## Preconditions

1. The deploy ref's CI is green (overlay-drift plus both images).
2. The configs prod host group renders the audit profile, a prod-specific
   `KAFKA_CLUSTER_ID`, the audit role passwords, and the audit DSNs. Confirm the
   prod `KAFKA_CLUSTER_ID` is distinct from QA's.
3. A full pre-deploy backup exists (the deploy path enforces this; do not bypass).
4. The same sequence was run on a freshly recreated QA and passed.

## Pre-flight checklist (go/no-go)

Validated on QA 2026-06-07 at `c81d89e`: the full sequence ran on a freshly
recreated QA and all eight audit acceptance behaviors reproduced (produce,
consume, chain link, notarize, recent-to-ClickHouse and old-to-Yugabyte routing,
ClickHouse-outage chain resilience).

- [x] Green deploy SHA `c81d89e` (build-push overlay-drift gate plus both images).
- [x] Fresh-boot first-boot sequence needs no manual Kafka topic creation and no
      audit-consumer crash-loop (TACK-301 and TACK-305 shipped in `c81d89e`).
- [x] Prod `KAFKA_CLUSTER_ID` is distinct from QA (`tack_servers.yml` vs
      `tack_qa_servers.yml`).
- [x] Audit profile renders unconditionally and `AUDIT_CONSUMER_CLICKHOUSE_DSN`
      (consumer write) targets the ClickHouse audit database.

Confirm at run time, not verifiable ahead of the deploy:

- [ ] A full pre-deploy backup of the prod auth+audit YugabyteDB exists. If
      `deploy-tack` does not take one, run the backup first; do not bypass.
- [ ] The prod vault provides `AUDIT_WRITER_PASSWORD`, `AUDIT_READER_PASSWORD`,
      and `AUDIT_REDACTOR_PASSWORD` (`seed-roles` errors loudly if missing).
- [ ] You have explicit authorization to mutate prod; this cutover is
      operator-run, never automated.

The audit-consumer needs no restart and creates its topic itself. The app holds
no ledger read connection: every ledger read and redaction is a host command
(`audit query`, `audit get`, `audit export`, `audit redact-actor`).

## Steps

Run from the configs repo for the deploy, and via the `tack-ops` container on the
prod host for the init steps. `tack-ops` reads `DATABASE_URL` and the audit
passwords from the rendered `.env`.

1. **Deploy the stack.** From `~/Sites/configs`, run the `deploy deploy-tack`
   action limited to the prod host group (`tack_servers`), passing the chosen
   green SHA for both `tack_commit` and `tack_image_tag` (same SHA for both: git
   supplies the stack, ghcr supplies the images). The current validated SHA is
   `c81d89e`; use it or a newer SHA with a green `build-push` run.

   ```
   cd ~/Sites/configs && go run goodkind.io/configs/cmd/configs deploy deploy-tack \
     --limit tack_servers \
     --extra-var tack_commit=<green-SHA> \
     --extra-var tack_image_tag=<green-SHA>
   ```

   This pulls the audit images and brings up kafka, clickhouse, and
   audit-consumer.

   The audit-consumer cannot authenticate to YugabyteDB until the audit roles
   exist (step 3). It waits for them on its own, logging
   `audit.consumer.yugabyte_not_ready` at WARN and staying up (no crash-loop),
   then proceeds automatically once step 3 runs (TACK-301).

2. **Migrate.** `docker compose run --rm tack-ops migrate`. This advances prod to
   the cutover migration version. Migrations run only through this path.

3. **Seed audit roles.** `docker compose run --rm tack-ops ops audit seed-roles`.
   Creates or rotates the LOGIN audit roles the app and audit-consumer
   authenticate as. Passwords come from the rendered `.env`. The audit-consumer
   picks up the new roles on its own (it pings until ready, then ensures the
   topic and starts, TACK-301), so it needs no restart. The app publishes to
   Kafka and holds no ledger read connection, so it needs no restart either.

4. **Kafka topic.** No manual step: the audit-consumer ensures `audit.events.v1`
   with 256 partitions (the shardOf width) and broker-default replication factor
   on startup once Yugabyte is reachable, treating an existing topic as a no-op
   (TACK-305). Confirm it exists after the consumer is healthy:
   `kafka-topics.sh --bootstrap-server localhost:9092 --describe --topic
   audit.events.v1` should report PartitionCount 256.

5. **ClickHouse schema.** No manual step: the audit-consumer creates the audit
   database and `audit.events_olap` on connect (`ensureClickHouseSchema`). Verify
   it exists after the consumer is healthy. The consumer is the only process
   that reads or writes ClickHouse (`AUDIT_CONSUMER_CLICKHOUSE_DSN`); ledger
   reads go to YugabyteDB through the host commands.

## Verify

- All audit services healthy: kafka, clickhouse, and audit-consumer `Up`; the
  consumer has joined its group and logs no real fetch errors (the idle-poll
  sentinel is no longer logged as of the consumer fix).
- One real product action produces exactly one `audit.events` row, the hash chain
  links (prev_hash chains to row_hash), and the notarizer signs a Merkle root.
- `audit query` (host command, reader role, `--execute`) returns that row from
  YugabyteDB.
- Kill ClickHouse briefly and confirm the chain keeps advancing (ClickHouse is
  best-effort, never blocks the canonical write).

## Safety

- Prod FoundationDB is already configured. NEVER run `fdbcli configure new` on
  prod; it is destructive.
- Preserve `audit.chain_heads` on any recovery so the hash chain continues.
- Do not echo or paste audit role passwords or DSNs.

## Rollback

The audit profile is additive: migration 003 only adds tables and indexes, and
the canonical product path never touches kafka, clickhouse, or the
audit-consumer. The two reversals below were exercised on QA on 2026-06-07; the
observed behavior is noted, not assumed. Run all commands on the prod host from
`/root/tack`.

**Tested fact (QA 2026-06-07):** with `kafka` stopped, a real product write
(`tack_create_project`) still committed and the tool returned `ok`
(`mcp.tool.completed`); the product path does not block on the audit producer.
The audit event for writes during the window is **dropped, not queued** (the
synchronous producer fails; the app logs `kafka.produce.failed` and
`audit.record_failed`), so a Kafka-down window is a deliberate audit gap.

### Reversal A: audit pipeline misbehaving, product path healthy (fast)

Stops audit ingestion immediately; product keeps serving.

```
docker compose stop audit-consumer kafka clickhouse
```

Accept that audit events during the stop window are not recorded. Use when the
audit pipeline is actively harmful and a short audit gap is tolerable.

### Reversal B: revert the cutover cleanly (no audit loss)

Return the app to the pre-cutover synchronous Yugabyte recorder by clearing the
Kafka producer, then redeploy. With `AUDIT_KAFKA_BROKERS` empty and
`AUDIT_WRITER_DSN` set, the app records audit synchronously to Yugabyte (its
pre-cutover behavior) and stops attempting Kafka produces.

1. In the configs repo, render the prod `.env` with `COMPOSE_PROFILES` lacking
   `audit` and `AUDIT_KAFKA_BROKERS` empty (revert the `tack.env.j2` audit block),
   and choose the pre-cutover image SHA for `tack_image_tag`.
2. Re-run the same `deploy deploy-tack --limit tack_servers` command as the
   cutover step 1 with that SHA. This recreates `app` on the prior image with the
   audit producer disabled and leaves kafka/clickhouse/audit-consumer stopped.

### Always

- Leave migration 003 in place. Its tables are additive and inert when the audit
  profile is off; rolling the schema back is unnecessary and would risk the auth
  tables in the same database.
- Preserve `audit.chain_heads`. Never drop it; the chain resumes from its last
  head when the audit profile is re-enabled.

After any real prod rollback, append the exact commands used and the observed
outcome here.
