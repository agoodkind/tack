# Cut over the production audit ledger

The production cutover succeeded on 2026-06-09 through
`deploy deploy-tack --limit tack_servers` at SHA `634562b`. That release pinned
YugabyteDB `2024.2.8.0-b85` under TACK-330 and changed only audit processing.
The consumer provisioned itself, became the sole ledger writer, and created
`audit.events.v1` with 256 partitions. The notarizer signed per-`(org, shard)`
Merkle roots, and a live MCP call produced chained ledger rows. The operator
did not need rollback. QA had passed all eight acceptance behaviors on June 7.

The deploy exited nonzero when its final `sysctl --system` handler tried
host-only `kernel.*` keys in the unprivileged LXC. Configs corrected that handler
to run `sysctl -p /etc/sysctl.d/99-tack-ipv6.conf`. TACK-18 and TACK-303 track
provisioning as the durable replacement for this manual sequence.

## Resolve values and confirm prerequisites

This procedure applies to the original production state before the audit
profile or cutover migration. FoundationDB and product data already existed.
Do not run destructive `fdbcli configure new` or repeat product seeding.

Resolve command values from current sources rather than copying historical
SHAs, passwords, hostnames, DSNs, or partition counts from this record.

- Select one commit/image SHA with a passing `build-push` run, including
  `overlay-drift` and both image builds. Preflight validated `c81d89e`.
- Read stack configuration from Tack `docker-compose.yml` and configs
  `tack/tack.env.j2` and production group variables. Verify the rendered `.env`.
- Require the production audit profile, a distinct production `KAFKA_CLUSTER_ID`,
  audit role passwords, and DSNs. Preflight compared `tack_servers.yml` with
  `tack_qa_servers.yml` and confirmed the distinct IDs and enabled services.
- Require matching `AUDIT_KAFKA_TOPIC` and `AUDIT_CONSUMER_KAFKA_TOPIC`, and
  read brokers from `AUDIT_KAFKA_BROKERS`.
- Derive partition count from `shardOf()` in `internal/audit/canonical.go`.
  At cutover it used the low CRC32 byte, or 256 shards. Create one partition
  per shard and update the count if TACK-306 changes shard width. Use the
  environment's broker count for replication factor, or 1 on a single broker.
- Read `AUDIT_WRITER_PASSWORD`, `AUDIT_READER_PASSWORD`, and
  `AUDIT_REDACTOR_PASSWORD` from the vault-rendered environment. Do not print
  them. Role seeding fails when they are absent.
- Require a full production auth-and-audit backup before deploying. Run the
  backup first if `deploy-tack` does not create it; do not bypass this requirement.
- Require successful execution on freshly recreated QA and explicit operator
  authorization for production mutation. Do not automate this cutover.

TACK-301 and TACK-305 in `c81d89e` eliminated manual topic creation and consumer
restart loops on fresh boot. The consumer waits for audit roles and creates
its topic automatically. The app does not open ledger read connections; host
commands `audit query`, `audit get`, `audit export`, and `audit redact-actor`
perform ledger reads and redactions.

## Execute the cutover

Run deployment from configs. Run initialization in the production `tack-ops`
container, which reads `DATABASE_URL` and audit passwords from `.env`.

1. Deploy the stack using one passing SHA for both `tack_commit` (stack source)
   and `tack_image_tag` (registry images):

   ```
   cd ~/Sites/configs && go run goodkind.io/configs/cmd/configs deploy deploy-tack \
     --limit tack_servers \
     --extra-var tack_commit=<green-SHA> \
     --extra-var tack_image_tag=<green-SHA>
   ```

   The deploy pulls images and starts Kafka and audit-consumer. Until step 3,
   the consumer remains running and logs `audit.consumer.yugabyte_not_ready`
   at WARN rather than repeatedly crashing.
2. Run `docker compose run --rm tack-ops migrate` to apply the cutover migration.
   Run migrations only through this path.
3. Run `docker compose run --rm tack-ops ops audit seed-roles` to create or rotate
   LOGIN roles using rendered passwords. The consumer pings until the roles
   work, creates its topic, and begins consumption. Neither it nor the app
   requires a restart; the app publishes to Kafka without ledger read access.
4. Confirm automatic topic creation after the consumer becomes healthy.
   At cutover, `kafka-topics.sh --bootstrap-server localhost:9092 --describe --topic
   audit.events.v1` must report PartitionCount 256. The consumer uses the
   shard width and broker-default replication factor and preserves an existing
   topic without modifying it.

## Verify recording

- Require Kafka and audit-consumer to report `Up`. Verify group membership and
  the absence of actual fetch errors. The fixed consumer does not log idle-poll
  sentinels as errors.
- Perform one real product action and require exactly one `audit.events` row,
  matching `prev_hash` and previous `row_hash`, and a signed Merkle root.
- Read that row through host `audit query` using the reader role and `--execute`.

Preserve `audit.chain_heads` during every recovery. Do not print or paste audit
passwords or DSNs.

## Roll back

Migration `003` adds tables and indexes without changing the canonical product
storage operations. QA exercised both reversals on 2026-06-07. Run rollback
commands on the production host from `/root/tack`.

On that QA test, stopping Kafka did not prevent `tack_create_project` from
committing or returning `ok` with `mcp.tool.completed`. The synchronous producer
failed and logged `kafka.produce.failed` and `audit.record_failed`. The app
**dropped rather than queued** audit events during that window. A Kafka stop
therefore creates an audit gap under this tested cutover release.

### Stop harmful audit ingestion

This action immediately stops ingestion while the product continues serving.
Use it only when a short audit gap is acceptable:

```
docker compose stop audit-consumer kafka
```

### Restore synchronous recording without audit loss

1. In configs, render production `.env` without `audit` in `COMPOSE_PROFILES`
   and with empty `AUDIT_KAFKA_BROKERS`. Revert the `tack.env.j2` audit block
   and select the pre-cutover image for `tack_image_tag`. Preserve
   `AUDIT_WRITER_DSN` for synchronous YugabyteDB recording.
2. Rerun `deploy deploy-tack --limit tack_servers` with that SHA. The deploy
   recreates the app on the prior image and stops Kafka and audit-consumer.
   The app then writes audit events synchronously to YugabyteDB.

Leave migration `003` in place. Its additive tables remain inactive without the
audit profile; schema rollback could endanger auth tables in the same database.
Never drop `audit.chain_heads`. Re-enabling the profile must continue from its
last heads. After a production rollback, append the exact commands and observed
results to this procedure.
