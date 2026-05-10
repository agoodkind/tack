# Phase 2 Wave 1 Runbook (dual-write)

This runbook walks an operator through deploying Phase 2 wave 1 of the
Tack audit refactor on CT 117 (single-host, N=1). Wave 1 is the
dual-write phase. The producer fans every audit event to the existing
Yugabyte-backed WAL path AND to a new Apache Kafka topic. A separate
`audit-consumer` projects the Kafka stream into a sibling table
`audit.events_v2`. A parity gate runs for 24 hours before wave 2
(cutover) is allowed.

The reader assumed here knows Tack's deploy model (`make deploy`,
docker compose on CT 117, env-only configuration) and the existing
audit/WAL surface. The reader has not necessarily read the design doc;
anything from it that wave 1 depends on is restated here.

References: `incident_2026-05-09_seed_parallel_org/audit_horizontal_design.md`
(sections 1, 5, 12.1, 12.2), `wave1_producer_implementation_report.md`,
and `wave1_consumer_implementation_report.md`, all in that same dir.

---

## 0. Compatibility note

This runbook reflects the deploy mechanics in place as of commit `a5aec6d`.
The current sanctioned deploy path is `make deploy`, which rsyncs the
source tree to CT 117 and builds the image on the remote. Hand-rolled
`rsync` invocations against production have been retired; do not invent
new ones. When the ops consolidation lands per
`incident_2026-05-09_seed_parallel_org/ops_consolidation_plan.md`
(`./server ops deploy`, image-based, registry-pushed), sections 4 and 5
will need a follow-up revision to swap `make deploy` for the new
subcommand. Until then, `make deploy` is the operator's only sanctioned
path and is treated as a moratorium-acknowledged exception for wave 1.

The parity gate referenced throughout this runbook is the Go subcommand
`./server ops audit parity`, implemented at
`internal/ops/audit_parity.go`. The earlier `scripts/audit-parity.sh`
referenced in design docs and prior drafts of this runbook never existed
in the tree and is forbidden by the shell-script moratorium. The Go
subcommand reads its time window from environment variables
(`TACK_PARITY_FROM`, `TACK_PARITY_TO`, optional `TACK_PARITY_THRESHOLD`)
rather than CLI flags.

---

## 1. Pre-flight checklist

All boxes must be true before wave 1 deploys. The verification commands
below are the canonical ones; do not substitute paraphrased equivalents.

- Phase 1 (WAL fix) is in production at commit `23ad44a` or a strict
  descendant, and has run there for at least 7 days clean (design doc
  wave 0). Confirm with:
  ```bash
  ssh tack 'docker inspect --format "{{ index .Config.Labels \"org.opencontainers.image.revision\" }}" tack-app-1'
  ```
  The output must show `23ad44a` or a commit that contains it.
- Backup system is working. From the operator's Mac:
  ```bash
  make backup
  ssh tack 'TS=$(cat /root/backups/.latest); ls -la /root/backups/tack-$TS/MANIFEST.txt'
  ssh tack 'TS=$(cat /root/backups/.latest); test -s /root/backups/tack-$TS/MANIFEST.txt && echo NON_EMPTY'
  ```
  The last line must print `NON_EMPTY`. An empty `MANIFEST.txt` is the
  2026-04-25 backup defect class and blocks the deploy.
- `make test` passes on the wave 1 branch. The `internal/audit`
  package must be green. Consumer integration tests gate on
  `AUDIT_CONSUMER_TEST_DSN`; they may skip on the operator host without
  invalidating this check.
- The wave 1 branch builds. From the repo root:
  ```bash
  bash -c "cd /Users/agoodkind/Sites/tack && /opt/homebrew/bin/go build ./cmd/server/"
  bash -c "cd /Users/agoodkind/Sites/tack && /opt/homebrew/bin/go build ./cmd/audit-consumer/"
  ```
  Both commands must exit zero.
- Compose services for `kafka`, `seaweedfs`, `clickhouse`, and
  `audit-consumer` are present in `docker-compose.yml`. Verify with:
  ```bash
  grep -E '^  (kafka|seaweedfs|clickhouse|audit-consumer):' docker-compose.yml
  ```
  All four service keys must appear.
- The parity subcommand is registered in the wave 1 binary. Build it and
  ask the ops dispatcher for help:
  ```bash
  bash -c "cd /Users/agoodkind/Sites/tack && /opt/homebrew/bin/go build ./cmd/server/ && ./server ops audit help"
  ```
  The help output must list `parity` as a recognized subcommand. The
  implementation lives at `internal/ops/audit_parity.go` and the
  dispatcher entry is at `internal/ops/command.go`. There is no
  `scripts/audit-parity.sh`; do not create one.

If any item fails, stop and resolve before continuing. Wave 1 has no
"partial pre-flight" mode.

---

## 2. One-time setup: Kafka cluster ID

Apache Kafka 4.x in KRaft mode requires a cluster ID baked into the
log-dir on first boot. Generate it once and pin it in `/root/tack/.env`.
A fresh ID on a recreate of the broker container reformats the log dir
and orphans every existing partition.

Run on the operator host (or any host with Docker):

```bash
docker run --rm apache/kafka:4.2.0 /opt/kafka/bin/kafka-storage.sh random-uuid
```

The output is a 22-character base64 UUID. Persist it:

```bash
ssh tack 'grep -q "^KAFKA_CLUSTER_ID=" /root/tack/.env || echo "KAFKA_CLUSTER_ID=<paste-the-uuid-here>" >> /root/tack/.env'
```

Then edit `/root/tack/.env` on `tack` and replace the placeholder with
the real value. After the first format the value is locked in for the life of
`kafka-data`. Never rotate without first wiping that volume, since
rotating-without-wiping puts the broker in a boot loop.

To verify after first start:

```bash
ssh tack 'docker compose exec kafka cat /var/lib/kafka/data/meta.properties | grep cluster.id'
```

The `cluster.id` must equal `KAFKA_CLUSTER_ID`.

---

## 3. Env var configuration

Append the following block to `/root/tack/.env`. `KAFKA_CLUSTER_ID`
comes from step 2; the rest are fixed defaults for wave 1 on N=1.

| Variable | Wave 1 value | Notes |
|---|---|---|
| `KAFKA_CLUSTER_ID` | from step 2 | KRaft cluster identity. Compose refuses to start `kafka` without it. |
| `AUDIT_KAFKA_BROKERS` | `kafka:9092` | Producer bootstrap. Empty value keeps the wave-0 (WAL-only) path. |
| `AUDIT_KAFKA_TOPIC` | `audit.events.v1` | Producer topic. |
| `AUDIT_KAFKA_CLIENT_ID` | `tack-audit-producer` | Producer client ID for broker-side logs. |
| `AUDIT_CONSUMER_KAFKA_BROKERS` | `kafka:9092` | Consumer bootstrap. |
| `AUDIT_CONSUMER_KAFKA_TOPIC` | `audit.events.v1` | Must equal the producer topic. |
| `AUDIT_CONSUMER_GROUP_ID` | `tack-audit-projector` | Group ID. Renaming this in wave 2 forces a re-projection from the earliest offset. |
| `AUDIT_CONSUMER_YUGABYTE_DSN` | same value as `AUDIT_WRITER_DSN` | Consumer projects into Yugabyte under the existing audit_writer role. |
| `AUDIT_CONSUMER_CLICKHOUSE_DSN` | `clickhouse://default:@clickhouse:9000/audit` | OLAP target. |
| `AUDIT_CONSUMER_SIGNING_KEY_PATH` | `/etc/tack/audit-signing.pem` | Ed25519 signing key for embedded notarizer. |

`AUDIT_KAFKA_BROKERS` is the rollback knob: empty or unset keeps the
producer on the wave-0 (WAL-only) path, while a non-empty value wraps
the WAL recorder in a `DualRecorder` whose primary is the Kafka
producer and secondary is the WAL. The branch is in
`cmd/server/main.go` `wrapAuditWithKafka`; it returns the unwrapped
`walRec` when the var is empty.

The Ed25519 key referenced by `AUDIT_CONSUMER_SIGNING_KEY_PATH` must
exist before deploy:

```bash
ssh tack 'test -f /etc/tack/audit-signing.pem && stat -c "%a %U" /etc/tack/audit-signing.pem'
```

The file must exist with mode `0600` and owner matching the user that
runs the `audit-consumer` container (default: `root` inside the
container). If absent, the consumer's notarizer disables itself
silently; this is acceptable in wave 1, but the parity gate ignores
notarization, so missing the key now means it must be staged before
wave 3.

After editing, lint the env file:

```bash
ssh tack 'grep -E "^(KAFKA_|AUDIT_KAFKA_|AUDIT_CONSUMER_)" /root/tack/.env'
```

Every row from the table above should appear exactly once.

---

## 4. Migration order

Wave 1 introduces three new SQL migrations. Run them BEFORE the new app
binary takes traffic. The migrations are forward-only and idempotent
(each guards on `IF NOT EXISTS` or a goose statement marker), but the
new binary expects the new tables to exist.

The order:

1. `migrations/003_audit_kafka_consumer.sql`: creates `audit.consumer_offsets`.
2. `migrations/004_audit_events_event_id_uniq.sql`: adds the
   `events_event_id_uniq` UNIQUE index on `audit.events(event_id)`. The
   migration aborts loudly if `audit.events` already contains duplicate
   `event_id` values; if that happens, fix the duplicates first.
3. `migrations/005_audit_events_v2_sibling.sql`: creates
   `audit.events_v2` (parity sibling) and `audit.events_v2_dlq`
   (malformed-payload landing).

To run the migrations, ship the wave 1 image to CT 117 with `make deploy`
and then invoke `./server migrate` inside the freshly-built container.
Hand-rolled `rsync` of a host-built binary to production is no longer
allowed; the operator builds inside the remote via `make deploy` and runs
the migrate command against the new image. The migration runner itself
is unchanged; no new flags.

```bash
make deploy
ssh tack 'cd /root/tack && docker compose run --rm --entrypoint /server app migrate'
```

The `docker compose run --rm` form runs the migrate command in an
ephemeral container against the same `tack-server:latest` image that the
running app uses, so the schema and binary line up. The migrate command
inherits `AUDIT_WRITER_DSN` (and any other relevant DSNs) from the
container's env, sourced from `/root/tack/.env`.

When the ops consolidation lands, the `make deploy` step above is
replaced by `./server ops deploy`. Until then, `make deploy` is the
sanctioned path; see section 0.

The migrate command prints one line per migration applied. Expected
output for a fresh wave 1 host:

```
applied migration 003_audit_kafka_consumer.sql
applied migration 004_audit_events_event_id_uniq.sql
applied migration 005_audit_events_v2_sibling.sql
```

If the host has previously run any of these (e.g. retry after a
partial deploy), goose skips them and prints nothing. That is fine.

After migrate completes, sanity check the schema:

```bash
ssh tack 'docker compose exec yugabyte ysqlsh -U yugabyte -d tack -c "\dt audit.*"'
```

The output must include `audit.events`, `audit.events_v2`,
`audit.events_v2_dlq`, `audit.consumer_offsets`, and the existing
`audit.chain_heads`, `audit.notarizations`, `audit.pii`.

---

## 5. Deploy sequence

The deploy fans out four new compose services and the dual-write app
binary. Order matters: the broker and storage layers come up first, the
consumer waits for them, the app binary ships last.

Step 1. Sync the wave 1 tree to CT 117 and build the app image. Use
`make deploy`; it runs the preflight, takes a backup, syncs the source
tree, and builds the new `tack-server:latest` image on the remote. Do
not invoke `rsync` directly against production. `make deploy` does NOT
pull the new service images for `kafka`, `seaweedfs`, or `clickhouse`;
compose pulls those on first start, which is why step 2 below pulls them
explicitly.

```bash
make deploy
```

When `./server ops deploy` lands per the ops consolidation plan, this
step becomes a `./server ops deploy` invocation with no rsync. See
section 0.

Step 2. Pull the new service images on CT 117 before flipping any
running container:

```bash
ssh tack 'cd /root/tack && docker compose pull kafka seaweedfs clickhouse'
```

Each image is large; this is the longest step. Pulling explicitly first
isolates network-reach failures from the deploy itself.

Step 3. Bring up Kafka first so it can format its log dir on the pinned
`KAFKA_CLUSTER_ID`:

```bash
ssh tack 'cd /root/tack && docker compose up -d kafka'
ssh tack 'cd /root/tack && docker compose logs --tail=200 kafka | grep -E "started|Loaded"'
```

Wait for the broker to log `Kafka Server started`. Then:

```bash
ssh tack 'cd /root/tack && docker compose exec kafka /opt/kafka/bin/kafka-topics.sh --bootstrap-server kafka:9092 --list'
```

The first run after migrate will print no topics. Create the topic
explicitly:

```bash
ssh tack 'cd /root/tack && docker compose exec kafka /opt/kafka/bin/kafka-topics.sh \
    --bootstrap-server kafka:9092 \
    --create --topic audit.events.v1 \
    --partitions 256 --replication-factor 1 \
    --config min.insync.replicas=1 \
    --config unclean.leader.election.enable=false'
```

The topic is auto-creatable by the producer; creating it explicitly
makes the partition count and ISR config deterministic on the first
event. Verify against the broker image's docs before changing any of
these flag values.

Step 4. Bring up SeaweedFS and ClickHouse:

```bash
ssh tack 'cd /root/tack && docker compose up -d seaweedfs clickhouse'
ssh tack 'cd /root/tack && docker compose ps seaweedfs clickhouse'
```

Both services must report state `running` and (for ClickHouse) health
`healthy`. SeaweedFS is wired in wave 1 only as a placeholder for
wave 4; it stays idle.

Step 5. Start the audit-consumer container. The image is the same
`tack-server:latest` built in step 1, with the entrypoint overridden to
`/usr/local/bin/audit-consumer`.

```bash
ssh tack 'cd /root/tack && docker compose up -d audit-consumer'
ssh tack 'cd /root/tack && docker compose logs --tail=100 audit-consumer'
```

The consumer logs an `audit_consumer.started` line on boot once
`audit.NewConsumer` returns without error
(`cmd/audit-consumer/main.go:103`). Confirm the consumer connected
before continuing:

```bash
ssh tack 'docker compose logs --since 2m audit-consumer | grep -q audit_consumer.started && echo CONSUMER_STARTED || echo CONSUMER_NOT_STARTED'
```

The output must read `CONSUMER_STARTED`. If it reads
`CONSUMER_NOT_STARTED`, the consumer's `docker compose ps` line will
still show `running`, but it is not actually consuming. Recheck the
consumer DSNs and broker bootstrap before proceeding. Once started, the
consumer stays idle until events arrive on the topic.

Step 6. Restart the app container so it picks up the new env vars and
binary:

```bash
ssh tack 'cd /root/tack && docker compose up -d app'
ssh tack 'docker logs --tail=200 tack-app-1 | grep -E "audit\.kafka_enabled|audit\.wal_enabled"'
```

On the first boot with `AUDIT_KAFKA_BROKERS` set, the app logs an
`audit.kafka_enabled` line with `broker_count` and `topic` fields
(`cmd/server/main.go:399`). The WAL-only path emits `audit.wal_enabled`
instead (`cmd/server/main.go:371`). There is no
`audit.recorder.dual_kafka_wal` log line in the binary; do not search
for it.

Confirm the wrap engaged before moving on:

```bash
ssh tack 'docker logs --since 5m tack-app-1 | grep -q audit.kafka_enabled && echo WRAP_ENGAGED || echo WRAP_NOT_ENGAGED'
```

The output must read `WRAP_ENGAGED`. If it reads `WRAP_NOT_ENGAGED`, the
producer is in WAL-only mode (likely an unset, empty, or whitespace
`AUDIT_KAFKA_BROKERS`); fix the env and restart before continuing.
Without this check, an unset broker var leaves the producer in WAL-only
mode and the operator only finds out when the smoke test in section 6
shows zero rows in `events_v2`.

---

## 6. Smoke test

Run two checks immediately after the app comes up. Both are required.

Test 1: One MCP read call. The simplest is `tack_list_workspaces`
because it returns quickly and emits a single read-class audit event.

```bash
ssh tack 'curl -sS -H "Authorization: Bearer $(grep ^TACK_DEV_TOKEN .env | cut -d= -f2-)" \
    -H "Content-Type: application/json" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"tack_list_workspaces\",\"arguments\":{}}}" \
    http://localhost:8000/mcp'
```

(Adjust the auth header to a real token if `TACK_DEV_TOKEN` is not how
your env is set up.) The call returns 200 with a JSON envelope.

Verify the event landed in BOTH tables:

```bash
ssh tack 'docker compose exec yugabyte ysqlsh -U yugabyte -d tack -c \
    "SELECT verb, count(*) FROM audit.events WHERE occurred_at > now() - interval '\''2 minutes'\'' GROUP BY verb;"'
ssh tack 'docker compose exec yugabyte ysqlsh -U yugabyte -d tack -c \
    "SELECT verb, count(*) FROM audit.events_v2 WHERE occurred_at > now() - interval '\''2 minutes'\'' GROUP BY verb;"'
```

The `workspace.list` row counts (the verb constant
`VerbWorkspaceList = "workspace.list"` at `internal/audit/verbs.go:38`;
there is no `tack.*` prefix anywhere in the codebase) must match
between the two tables. A small lag (under 5 seconds) on `events_v2`
is expected and acceptable; that is the consumer commit cadence.

After the smoke call, also confirm the consumer is committing offsets:

```bash
ssh tack 'docker compose exec yugabyte ysqlsh -U yugabyte -d tack -c \
    "SELECT consumer_group, topic, partition, \"offset\", updated_at FROM audit.consumer_offsets ORDER BY updated_at DESC LIMIT 5;"'
```

At least one row must appear with a recent `updated_at`. An empty
result means the consumer is processing but never committing, and a
restart will re-read from the earliest offset.

Test 2: The parity scan. Wave 1 uses the `./server ops audit parity`
subcommand (`internal/ops/audit_parity.go`). It reads its window from
environment variables: `TACK_PARITY_FROM` and `TACK_PARITY_TO` are
ISO-8601 UTC timestamps, and the optional `TACK_PARITY_THRESHOLD`
defaults to `1.0` (perfect parity). The command emits a JSON result on
stdout and exits non-zero when the matched fraction is below threshold.

For a 10-minute smoke window, run on the operator host:

```bash
PARITY_FROM=$(date -u -v-10M +%Y-%m-%dT%H:%M:%SZ)
PARITY_TO=$(date -u +%Y-%m-%dT%H:%M:%SZ)
ssh tack "cd /root/tack && docker compose exec \
    -e TACK_PARITY_FROM=$PARITY_FROM \
    -e TACK_PARITY_TO=$PARITY_TO \
    -e TACK_PARITY_THRESHOLD=1.0 \
    app /server ops audit parity"
```

(The `date -u -v-10M` form is BSD `date` on macOS. On a GNU `date`
host, use `date -u -d '10 minutes ago' +%Y-%m-%dT%H:%M:%SZ`.)

Exit code zero means parity. Any other exit code aborts wave 1; rerun
after addressing the drift, and only proceed when zero is observed.

---

## 7. Parity gate (24-hour soak)

Wave 1 stays in dual-write for 24 hours of clean parity before wave 2 is
allowed. The soak window is enforced by re-running the parity
subcommand on a one-hour cadence.

Schedule the loop in a screen or tmux on the operator host. Each
iteration computes a fresh one-hour rolling window from environment
variables and feeds them to `./server ops audit parity`:

```bash
while true; do
  PARITY_FROM=$(date -u -v-1H +%Y-%m-%dT%H:%M:%SZ)
  PARITY_TO=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  printf "[%s] window=%s..%s " "$(date -u +%FT%TZ)" "$PARITY_FROM" "$PARITY_TO"
  ssh tack "cd /root/tack && docker compose exec \
      -e TACK_PARITY_FROM=$PARITY_FROM \
      -e TACK_PARITY_TO=$PARITY_TO \
      -e TACK_PARITY_THRESHOLD=1.0 \
      app /server ops audit parity" || echo "DRIFT"
  sleep 3600
done
```

(GNU `date` host: replace `date -u -v-1H +%Y-%m-%dT%H:%M:%SZ` with
`date -u -d '1 hour ago' +%Y-%m-%dT%H:%M:%SZ`.)

Expected: every iteration prints the window, emits a JSON result, and
exits zero. The `matched_fraction` field equals `1.0` and
`counts.only_legacy`, `counts.only_v2`, and `counts.content_diff` are
all zero for that window.

If any iteration prints `DRIFT`, the operator has up to one hour to
diagnose and resolve before reverting per section 9. Common causes are
in section 10.

Aux checks during the soak:

- Producer error rate. The counter is the expvar map
  `tack_audit_kafka_produce_total{result="error"}` defined at
  `internal/telemetry/metrics.go:135` and incremented at
  `internal/telemetry/metrics.go:170-172`. Metrics are exposed at
  `/debug/vars` on the app (`cmd/server/main.go:284`), not a Prometheus
  `/metrics` endpoint. Pull the JSON and confirm the `error` bucket
  stays at zero across the 24-hour window:
  ```bash
  ssh tack 'curl -sS http://localhost:8000/debug/vars | jq ".tack_audit_kafka_produce_total.error // 0"'
  ```
  The output must remain `0` for the duration.
- Dual-write skew. The histogram
  `tack_audit_dual_write_skew_seconds` (and its sum/count companion)
  exposes the per-event skew between Kafka and WAL acks
  (`internal/telemetry/metrics.go:155-156`). After the smoke test, read
  it once and confirm the p99 is well under one second at the current
  0.37 EPS load; a sustained higher value indicates the WAL is
  back-pressuring or the Kafka producer is starved.
- Consumer lag. Visible with the broker CLI:
  ```bash
  ssh tack 'docker compose exec kafka /opt/kafka/bin/kafka-consumer-groups.sh \
      --bootstrap-server kafka:9092 \
      --describe --group tack-audit-projector'
  ```
  The `LAG` column should stay under five seconds' worth of throughput.
  At Tack's current rate (around 0.37 EPS) that is roughly two events.
  The consumer also emits a `consumer.lag.high` warning line
  (`internal/audit/consumer.go:329`) once lag exceeds the configured
  warn threshold.

---

## 8. Wave 1 exit criteria

Wave 1 is successful and wave 2 is unblocked when ALL of the following
are true at the end of the 24-hour soak:

- 24 consecutive hours of `./server ops audit parity` exit-zero, no
  `DRIFT` iterations.
- Consumer lag p99 under 5 seconds, measured every hour from
  `kafka-consumer-groups.sh --describe` output. (The 5-second target is
  the design doc section 12.1 wave 1 target; revisit if production
  EPS climbs above 100.)
- Zero `kafka.produce.failed` warnings in `tack-app-1` logs over the
  soak window. The actual emitted log key is `kafka.produce.failed`
  (`internal/audit/kafka_recorder.go:130`); there is no `audit.` prefix
  on this line.
  ```bash
  ssh tack 'docker logs --since 24h tack-app-1 | grep -c kafka.produce.failed'
  ```
  Output must be `0`. Cross-check the same window against the expvar
  counter:
  ```bash
  ssh tack 'curl -sS http://localhost:8000/debug/vars | jq ".tack_audit_kafka_produce_total.error // 0"'
  ```
  Output must also be `0`.
- Zero `consumer.lag.high` warnings in `audit-consumer` logs over the
  soak window. The actual emitted log key is `consumer.lag.high`
  (`internal/audit/consumer.go:329`); there is no `audit.consumer.stalled`
  line in the binary.
  ```bash
  ssh tack 'docker compose logs --since 24h audit-consumer | grep -c consumer.lag.high'
  ```
  Output must be `0`. Also confirm no consumer-side hard errors:
  ```bash
  ssh tack 'docker compose logs --since 24h audit-consumer | grep -cE "audit\.consumer\.(commit_failed|project_failed|fetch_err)"'
  ```
  Output must be `0` (these are the lines emitted at
  `internal/audit/consumer.go:293`, `:278`, and `:260` respectively).
- Backup taken at the end of the soak captures both tables:
  ```bash
  make backup
  ssh tack 'TS=$(cat /root/backups/.latest); grep -E "audit.(events|events_v2|consumer_offsets)" /root/backups/tack-$TS/MANIFEST.txt'
  ```

When all criteria are satisfied, write the wave 1 closeout entry into
`incident_2026-05-09_seed_parallel_org/retro_log.md` and tag the deploy
commit with `phase2-wave1-complete`.

---

## 9. Rollback procedure

Wave 1 is fully reversible. The producer stays in dual-write mode only
while `AUDIT_KAFKA_BROKERS` is non-empty; clearing the var reverts to
WAL-only on the next app restart. Drift sustained beyond one hour, or
any of the failure modes in section 10, triggers this rollback.

Run, in this order. The pre-restart `stop` gives the Kafka producer
a chance to flush its in-flight buffer via `KafkaRecorder.Close`
(`internal/audit/kafka_recorder.go:178`); a hard `restart` would skip
that drain.

```bash
ssh tack 'sed -i.bak "s/^AUDIT_KAFKA_BROKERS=.*/AUDIT_KAFKA_BROKERS=/" /root/tack/.env'
ssh tack 'cd /root/tack && docker compose stop --timeout 15 app'
ssh tack 'cd /root/tack && docker compose up -d app'
ssh tack 'cd /root/tack && docker compose stop audit-consumer'
```

Verify the producer is back to WAL-only:

```bash
ssh tack 'docker logs --since 5m tack-app-1 | grep -E "audit\.kafka_enabled|audit\.wal_enabled"'
```

The log output must show `audit.wal_enabled`
(`cmd/server/main.go:371`) and must NOT show a fresh
`audit.kafka_enabled` line after the restart. There is no
`audit.recorder.wal_only` log line in the binary; do not search for it.

Optional cleanup (only after the team has decided not to retry wave 1
for at least 7 days):

```bash
ssh tack 'docker compose exec yugabyte ysqlsh -U yugabyte -d tack -c \
    "DROP TABLE IF EXISTS audit.events_v2_dlq; DROP TABLE IF EXISTS audit.events_v2;"'
```

This drops the migration 005 sibling table. Migration 004 (UNIQUE on
`event_id`) is a forward-only schema lock; do NOT drop the index, since
wave 2 also relies on it.

The Compose tag rollback path. If the app binary itself is suspect (not
just the Kafka leg), revert the running app container to the previous
image:

```bash
ssh tack 'docker tag tack-server:previous tack-server:latest && docker compose up -d --no-build app'
```

This assumes the deploy retains a `:previous` tag; verify that tag
exists before relying on it.

---

## 10. Failure modes and what they look like

| Failure | Symptom in logs | Symptom in metrics | Operator action |
|---|---|---|---|
| Broker down (kafka container exited) | `tack-app-1` logs `kafka.produce.failed` per call (`internal/audit/kafka_recorder.go:130`); MCP callers see a 500 on read-class verbs | Expvar map `tack_audit_kafka_produce_total` `{result="error"}` bucket climbs; consumer lag undefined (no broker) | Restart `kafka` service. If it boot-loops, check `KAFKA_CLUSTER_ID` against `meta.properties` per section 2. If diverged, the volume must be wiped and re-formatted (data loss on the topic; producer falls back to WAL automatically). |
| Consumer behind | `audit-consumer` logs `consumer.lag.high` warnings (`internal/audit/consumer.go:329`) | `kafka-consumer-groups.sh --describe` shows `LAG` growing; the parity scan reports `events_v2` count behind `events` count for recent windows | Check `audit-consumer` CPU and Yugabyte write latency. If Yugabyte is the bottleneck, the producer is unaffected; the WAL leg keeps writing. Increase `AUDIT_CONSUMER_BATCH_SIZE` or scale the consumer. |
| ClickHouse down | `audit-consumer` emits hard-error lines from the `audit.consumer.clickhouse_*_failed` family (`internal/audit/consumer.go:178-192`); no impact on Yugabyte projection | The `tack_audit_consumer_processed_total` map's `error` bucket climbs (`internal/telemetry/metrics.go:145`); `events_v2` keeps growing | ClickHouse is best-effort in wave 1 (it is fully wired in wave 3). The wave 1 parity gate looks at Yugabyte only, so this does not abort wave 1. Restart `clickhouse`; if data loss is suspected, drop and re-create the OLAP table since wave 1 does not yet read from it. |
| Schema mismatch | Parity scan reports drift on specific verbs (e.g. all `node.create` rows missing one column) | `events_v2` row count matches `events` but `row_hash` mismatches on those verbs (visible in the `content_diff_examples` array of the parity JSON) | The producer or consumer is on a stale schema. Check that all three wave 1 migrations actually applied (section 4). If migration 005 ran with an older `audit.events` definition, the sibling will be missing a column and every row will mismatch. Roll back per section 9 and re-apply migrations from a clean wave 1 binary. |
| Producer in WAL-only after restart | `tack-app-1` shows `audit.wal_enabled` but no `audit.kafka_enabled` line (`cmd/server/main.go:371`, `:399`) | Kafka topic stays empty under load; `events_v2` count stays flat while `events` keeps growing | `AUDIT_KAFKA_BROKERS` is unset, empty, or has stray whitespace. Fix the env in `/root/tack/.env` and restart `app`. Also possible: `audit.kafka_setup_failed` logged at `cmd/server/main.go:390`, in which case the wrap silently fell back to WAL; inspect that line for the underlying error. |
| Dual-write divergence | `tack-app-1` logs `dual.write.divergence` warnings (`internal/audit/dual.go:76`) when one leg succeeds and the other fails | The `tack_audit_dual_write_total` map shows a non-trivial gap between the `primary` and `secondary` paths (`internal/telemetry/metrics.go:152-153`) | Inspect the divergence log lines for the failing leg. If Kafka is the failing leg, treat as the broker-down row above. If the WAL is the failing leg, treat as a Phase 1 regression and escalate. |

Two more cases worth noting, even though they should never occur in
wave 1 on N=1:

- Consumer offset corruption. If `audit.consumer_offsets` is wiped or
  loses rows, the consumer re-reads from the earliest topic offset on
  next start. The UNIQUE index from migration 004 dedupes the
  reprojection, so this is recoverable; expect a one-time burst of
  consumer activity and several minutes of elevated lag.
- Notarizer key missing or malformed. The consumer gates the notarizer
  behind `cfg.SigningKeyPath != ""` (`internal/audit/consumer.go:131`)
  and silently skips notarizer construction when the path is empty;
  there is no dedicated `audit.notarizer.disabled` log line in the
  current binary. With a path set but a malformed key, expect
  `audit.consumer.notarizer_failed` lines on the 60-second notarizer
  tick (`AUDIT_CONSUMER_NOTARIZER_PERIOD`). Projection continues either
  way. Wave 1 does not gate on notarization. Re-stage the key before
  wave 3.

---

## 11. What this runbook deliberately does not cover

- Wave 2 cutover (producer stops writing to WAL): future
  `docs/phase2-wave2-runbook.md`.
- ClickHouse OLAP read-path wiring: wave 3.
- Iceberg/SeaweedFS cold archive: wave 4.
- Migration to N=many: design doc section 13.
- Backup-and-restore drills for the new tables beyond the smoke check
  in section 8: `docs/backup-runbook.md` remains authoritative.

If something is missing, check the design doc first, then ask before
extrapolating. Wave 1 is narrow on purpose.
