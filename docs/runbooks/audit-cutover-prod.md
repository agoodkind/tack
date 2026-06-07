# Prod audit cutover runbook (PROVISIONAL)

Status: **provisional, not yet executed on prod.** Derived from the QA cutover on
2026-06-07. The durable replacement for this manual sequence is the provision
entrypoint tracked in epic TACK-18 / TACK-303 ("deploy is not provision"); once
that lands, most of this becomes one idempotent command. Until then, follow this
by hand, and validate the whole sequence on a freshly recreated QA first (QA-first
policy).

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

## Steps

Run from the configs repo for the deploy, and via the `tack-ops` container on the
prod host for the init steps. `tack-ops` reads `DATABASE_URL` and the audit
passwords from the rendered `.env`.

1. **Deploy the stack.** From `~/Sites/configs`, run the `deploy deploy-tack`
   action limited to the prod host group, passing the chosen SHA for both
   `tack_commit` and `tack_image_tag` (same SHA for both: git supplies the stack,
   ghcr supplies the images). This pulls the audit images and brings up kafka,
   clickhouse, and audit-consumer.

   Expect the audit-consumer to crash-loop briefly here: it cannot authenticate to
   YugabyteDB until the audit roles exist (step 3). This is the known startup
   window (TACK-301), not a failure.

2. **Migrate.** `docker compose run --rm tack-ops migrate`. This advances prod to
   the cutover migration version. Migrations run only through this path.

3. **Seed audit roles.** `docker compose run --rm tack-ops ops audit seed-roles`.
   Creates or rotates the LOGIN audit roles the app and audit-consumer
   authenticate as. Passwords come from the rendered `.env`. After this, restart
   the audit-consumer so it reconnects with working credentials.

4. **Create the Kafka topic.** Until the topic-ensure is automated (TACK-305),
   create it explicitly on the broker: the topic name from `AUDIT_KAFKA_TOPIC`,
   partitions equal to the shard count (see Sources of truth), replication factor
   for the environment. Use the kafka tooling inside the `kafka` container against
   `AUDIT_KAFKA_BROKERS`. Creating an existing topic is a safe no-op.

5. **ClickHouse schema.** No manual step: the audit-consumer creates the audit
   database and `audit.events_olap` on connect (`ensureClickHouseSchema`). Verify
   it exists after the consumer is healthy. CAVEAT to check before trusting reads:
   the consumer write DSN (`AUDIT_CONSUMER_CLICKHOUSE_DSN`) and the app read DSN
   (`AUDIT_CLICKHOUSE_DSN`) must point at the SAME ClickHouse database, or
   `tack_audit_query` reads an empty table while the consumer writes elsewhere.
   Confirm both DSNs agree on the database name.

## Verify

- All audit services healthy: kafka, clickhouse, and audit-consumer `Up`; the
  consumer has joined its group and logs no real fetch errors (the idle-poll
  sentinel is no longer logged as of the consumer fix).
- One real product action produces exactly one `audit.events` row, the hash chain
  links (prev_hash chains to row_hash), and the notarizer signs a Merkle root.
- `tack_audit_query` routes recent-window reads to ClickHouse and older reads to
  YugabyteDB.
- Kill ClickHouse briefly and confirm the chain keeps advancing (ClickHouse is
  best-effort, never blocks the canonical write).

## Safety

- Prod FoundationDB is already configured. NEVER run `fdbcli configure new` on
  prod; it is destructive.
- Preserve `audit.chain_heads` on any recovery so the hash chain continues.
- Do not echo or paste audit role passwords or DSNs.

## Rollback

The audit profile is additive. If the cutover misbehaves, the canonical product
path does not depend on the audit-consumer (audit is layered defense). Disabling
the audit profile and reverting to the prior image/migration is the coarse
rollback; record the exact reversal once this runbook is executed and made
non-provisional.
