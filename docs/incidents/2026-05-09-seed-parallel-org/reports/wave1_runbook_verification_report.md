# Wave 1 runbook verification report

Verifier role: read-only audit of `docs/phase2-wave1-runbook.md` against the
checked-in code, Makefile, Compose file, and migrations on branch
`phase2-wave1-rebase`. No edits to runbook source. Cited file:line evidence
inline. Plain English, no em or en dashes, no bare double-hyphen sequences in
prose.

---

## 1. Verdict

The runbook is **partially safe** to follow today, and unsafe as a
copy-paste script. About 70 percent of the steps line up with what the code
and compose file actually expect. The remaining 30 percent collide with two
new rules the operator has set since the runbook was drafted (no new shell
scripts, no rsync of source to production), and a handful of strings
(verb names, log line names, smoke-test commands) drift from what the binary
actually emits.

The biggest single defect is that section 6 and section 7 both call
`scripts/audit-parity.sh`, a file that does not exist anywhere in the
repository. The replacement command (`./server ops audit parity`) is already
landed in `internal/ops/audit_parity.go` and dispatched by
`internal/ops/command.go:158-160`, so the runbook is pointing at a
non-existent shell script when a working Go subcommand is sitting next to
it.

Most other defects are small string drift (verb names, log line text,
metric names) or inherited shell or rsync usage in `make backup` and
`make deploy` that the consolidation plan is in the middle of replacing.

If an operator must run the wave today, they should treat sections 6 and 7
as TODO, and substitute `./server ops audit parity` (with the env vars
documented in `internal/ops/audit_parity.go:22-27`) for every reference to
`scripts/audit-parity.sh`. Sections 1, 2, 3, 4, 5, and 9 are mostly safe
once the rsync and shell-script issues in section 4 step 3 and section 5
step 1 are acknowledged.

---

## 2. Confirmed claims

| Runbook claim | Evidence |
|---|---|
| `make backup` target exists | `Makefile:144-147` (defines `backup:` and rsyncs scripts then runs `bash /root/tack/scripts/backup.sh`). |
| `make deploy` target exists | `Makefile:123-135` (defines `deploy: deploy-preflight backup` then rsyncs source and runs `docker build` and `docker compose up -d` on the host). |
| `make deploy-preflight` target exists | `Makefile:137-140`. |
| `make build` target exists | `.make/go.mk:138` defines `build: $(default-build-deps)`. The shim is fetched at parse time by `bootstrap.mk` so the target is reachable from the project Makefile. |
| `make test` target exists | `.make/go.mk:528-529` defines `test:` calling `go test $(GO_TEST_TARGETS)`. |
| `AUDIT_KAFKA_BROKERS` env var | `internal/config/config.go:100`. Empty string disables; matches the runbook's rollback contract. |
| `AUDIT_KAFKA_TOPIC` default `audit.events.v1` | `internal/config/config.go:101`. |
| `AUDIT_CONSUMER_KAFKA_BROKERS` is required | `cmd/audit-consumer/main.go:23` declares `AUDIT_CONSUMER_KAFKA_BROKERS,required`. The runbook's wave 1 value `kafka:9092` matches the compose default at `docker-compose.yml:285`. |
| `AUDIT_CONSUMER_KAFKA_TOPIC` default | `internal/config/config.go:119`, `cmd/audit-consumer/main.go:24`. Both default to `audit.events.v1`. |
| `AUDIT_CONSUMER_GROUP_ID` default `tack-audit-projector` | `internal/config/config.go:120`, `cmd/audit-consumer/main.go:25`. |
| `AUDIT_CONSUMER_YUGABYTE_DSN` is required | `cmd/audit-consumer/main.go:28`. The compose service plumbs `${AUDIT_WRITER_DSN}` through at `docker-compose.yml:290`. |
| `AUDIT_CONSUMER_CLICKHOUSE_DSN` env var | `internal/config/config.go:124`, `cmd/audit-consumer/main.go:29`. |
| `AUDIT_CONSUMER_SIGNING_KEY_PATH` env var | `internal/config/config.go:125`, `cmd/audit-consumer/main.go:30`. |
| `KAFKA_CLUSTER_ID` is required by compose | `docker-compose.yml:234` (`CLUSTER_ID: ${KAFKA_CLUSTER_ID:?...}`). |
| Compose service `kafka` exists | `docker-compose.yml:214` with image `apache/kafka:4.2.0` at line 215. |
| Compose service `seaweedfs` exists | `docker-compose.yml:251` with image `chrislusf/seaweedfs:3.71`. |
| Compose service `clickhouse` exists | `docker-compose.yml:261` with image `clickhouse/clickhouse-server:24.8`. |
| Compose service `audit-consumer` exists | `docker-compose.yml:279` with same `tack-server:latest` image and entrypoint override at line 283. |
| `audit-consumer` depends on `kafka`, `yugabyte`, `clickhouse` | `docker-compose.yml:302-308`. |
| Migration `003_audit_kafka_consumer.sql` exists and creates `audit.consumer_offsets` | `migrations/003_audit_kafka_consumer.sql:3-10`. |
| Migration `004_audit_events_event_id_uniq.sql` adds `events_event_id_uniq` UNIQUE on `audit.events(event_id)` and aborts on duplicates | `migrations/004_audit_events_event_id_uniq.sql:3-18`. |
| Migration `005_audit_events_v2_sibling.sql` creates `audit.events_v2` and `audit.events_v2_dlq` | `migrations/005_audit_events_v2_sibling.sql:3-67`. |
| Producer wrapper `wrapAuditWithKafka` returns `walRec` unchanged when `AUDIT_KAFKA_BROKERS` is empty | `cmd/server/main.go:378-404` (specifically `return walRec` at line 381 when `len(brokers) == 0`). |
| `DualRecorder` order is Primary then Secondary, with Primary being Kafka | `internal/audit/dual.go:23-37` plus the call site at `cmd/server/main.go:393` (`NewDualRecorder(kafkaRec, walRec)`). |
| Compose plumbs `AUDIT_KAFKA_BROKERS` into the `app` container | `docker-compose.yml:38`. |

---

## 3. Defects found

| Runbook claim | Why it is wrong | Recommended fix |
|---|---|---|
| Section 1 pre-flight: `test -x scripts/audit-parity.sh && echo OK` | The file does not exist. `ls scripts/` shows no `audit-parity.sh`. The shell-script moratorium forbids creating one. | Replace the pre-flight check with a build verification of the Go subcommand: `go build ./cmd/server/ && ./server ops audit help` (the dispatcher prints `usage: ./server ops audit parity` per `internal/ops/command_help.go:127`). |
| Section 6 Test 2: `bash /root/tack/scripts/audit-parity.sh --window=10m` | Same file does not exist. The replacement subcommand is wired at `internal/ops/command.go:158-160` and consumes `TACK_PARITY_FROM`, `TACK_PARITY_TO`, optional `TACK_PARITY_THRESHOLD` per `internal/ops/audit_parity.go:22-27`. There is no `--window` flag; the window is two RFC3339 timestamps in env. | Replace with: `ssh tack 'cd /root/tack && docker compose exec app /server ops audit parity'` and set `TACK_PARITY_FROM` / `TACK_PARITY_TO` in the call's environment. The command emits JSON to stdout and exits non-zero when matched-fraction is below threshold (`internal/ops/audit_parity.go:89-93`). |
| Section 7 24-hour soak loop calls `audit-parity.sh` once per hour | Same as above. | Same replacement; the loop body becomes a `docker compose exec app /server ops audit parity` call with rolling `TACK_PARITY_FROM` and `TACK_PARITY_TO` env values. |
| Section 4 step 3: `make build && rsync -az dist/tack tack:/root/tack/dist/tack-wave1` | The rsync ban applies to source rsync to prod. The runbook line is rsyncing a built binary, but this is still a host-build-then-push pattern and conflicts with the deploy model in `Makefile:123-135` that builds inside the container on the remote. There is no precedent in the Makefile for shipping a pre-built `dist/tack`. The `audit-consumer` target at `Makefile:163-167` only builds locally. | Drop the host-side build entirely. Run migrations via `docker compose exec app /server migrate` after `make deploy` ships the new image. Or, if you need to apply migrations before the new app starts taking traffic, run them inside an ephemeral container off the freshly-built `tack-server:latest` image (`docker compose run --rm --entrypoint /server app migrate`). |
| Section 5 step 1: bare `rsync -az --delete --exclude=...` to `tack:/root/tack/` | This is exactly the source-to-prod rsync the user has banned. It also duplicates what `make deploy` would do (`Makefile:129`), without taking the backup-as-prereq dependency that `make deploy` pulls in. | Either remove this step in favor of `make deploy` (which already includes a backup as a hard prereq) or wait for the consolidation plan's `./server ops deploy` replacement (referenced in `incident_2026-05-09_seed_parallel_org/ops_consolidation_plan.md:1-50`). The plan explicitly states the replacement does not rsync source. |
| Section 5 step 5: `make deploy` plus `docker compose up -d audit-consumer` | `make deploy` itself runs `rsync -az --delete ... tack:/root/tack/` per `Makefile:129`. Same rsync ban applies. | Same as above. Wait for `./server ops deploy` or use a moratorium-acknowledged exception. |
| Section 8 closeout uses `make backup` | `make backup` rsyncs `scripts/backup.sh` and then runs `bash /root/tack/scripts/backup.sh` (`Makefile:146-147`). The shell-script moratorium covers backup.sh; the consolidation plan flags the backup CLI as a future `./server ops backup` (see `ops_consolidation_plan.md:1-50`). | Either keep the shell path under a moratorium-acknowledged exception during wave 1, or wait for `./server ops backup` to land. Document the exception explicitly in the runbook so an operator does not file a fresh moratorium violation when running wave 1 closeout. |
| Section 6 Test 1 expected verb name `tack.workspace.listed` | The actual constant is `VerbWorkspaceList Verb = "workspace.list"` at `internal/audit/verbs.go:38`. The package contains no `tack.*` prefix and no `.listed` past-tense verb. | Use `workspace.list` in the SQL filter (or whichever verb is actually emitted by `tack_list_workspaces`; see the action shape recorded in `audit.events`). Or drop the verb filter and just count rows by `occurred_at`. |
| Section 5 step 6 expected log line `audit.recorder.dual_kafka_wal` | No such log message exists in the binary. The actual line emitted when the wrap engages is `audit.kafka_enabled` with `broker_count` and `topic` fields, at `cmd/server/main.go:399-402`. The WAL-only path emits `audit.wal_enabled` at `cmd/server/main.go:371`. There is no `dual_kafka_wal` or `wal_only` log line anywhere. | Replace the expected text with `audit.kafka_enabled` (and `audit.wal_enabled` for the rollback verification in section 9). |
| Section 9 rollback expected log line `audit.recorder.wal_only` | Same as above; the log line does not exist. | Replace with `audit.wal_enabled` and / or the absence of `audit.kafka_enabled` after restart. |
| Section 7 metric name `audit.kafka.produce_failed_total` | The actual expvar map key is `tack_audit_kafka_produce_total{result="ok|error"}` at `internal/telemetry/metrics.go:135` and `:170-172`. There is no `produce_failed_total` counter; the failure case is the `result="error"` branch. | Replace with: `tack_audit_kafka_produce_total{result="error"}`. The exact scrape path depends on what the operator uses to read expvar; the runbook should not name a series that the binary does not export. |
| Section 8 closeout: `docker logs --since 24h tack-app-1 \| grep -c audit.kafka.produce_failed` | The actual log message is `kafka.produce.failed` (with no `audit.` prefix) at `internal/audit/kafka_recorder.go:130`. Grepping for `audit.kafka.produce_failed` will return zero even when failures are present, masking a real outage. | Replace with `kafka.produce.failed`, or grep for `produce_total{result="error"}` from expvar. |
| Section 8 closeout: `grep -c audit.consumer.stalled` | No such log line exists. The closest consumer health signal is `consumer.lag.high` (`internal/audit/consumer.go:326`), or any of the `audit.consumer.*_failed` lines (e.g. `audit.consumer.commit_failed` at `internal/audit/consumer.go:293`, `audit.consumer.project_failed` at `:278`, `audit.consumer.fetch_err` at `:260`). | Replace `audit.consumer.stalled` with `consumer.lag.high` for lag, or grep across the family `audit.consumer.*_failed` for hard errors. |
| Section 10: `audit.consumer.batch_lag_high` and `audit.consumer.clickhouse_unavailable` and `audit.consumer.clickhouse_errors_total` | None of these strings exist. The lag warning is `consumer.lag.high`. ClickHouse failures use `audit.consumer.clickhouse_*_failed` family (`internal/audit/consumer.go:178-192` and `:467-470`). There is no `clickhouse_errors_total` counter; the closest series is the `audit_consumer_processed_total{result=...}` map at `internal/telemetry/metrics.go:145`. | Replace with the actual log keys before relying on these for triage. |
| Section 10 last bullet: `audit.notarizer.disabled` log line for missing key | No code path emits exactly `audit.notarizer.disabled`. The notarizer is gated behind `cfg.SigningKeyPath != ""` at `internal/audit/consumer.go:131`, so an absent key simply skips notarizer construction without a dedicated log line. | Either log the absence in code (a small follow-up patch) or document this as a silent skip and remove the log line claim from the runbook. |
| Section 2 verification command: `cat /var/lib/kafka/data/meta.properties` | The Apache Kafka 4.x image does write `meta.properties` to its `KAFKA_LOG_DIRS`, which is `/var/lib/kafka/data` per `docker-compose.yml:226`. The path is correct in shape, but the runbook should pull the cluster ID from the `cluster.id=...` line (which is what the grep does) and assert that it matches the env var. The command shape itself is fine; flagging because the runbook does not include `set -e` or any check, so a typo in `KAFKA_CLUSTER_ID` will silently print the broker-formatted value without failing. | Optionally tighten to `[ "$(grep ^cluster.id /var/lib/kafka/data/meta.properties \| cut -d= -f2)" = "$KAFKA_CLUSTER_ID" ] \|\| exit 1`. |
| Section 3 signing key contract | The runbook says the consumer expects `/etc/tack/audit-signing.pem`. That matches `docker-compose.yml:292` (default value) and `cmd/audit-consumer/main.go:30`. So this claim is right in shape. The defect is the runbook's "owner matching the user that runs the audit-consumer container (default: root inside the container)" claim: the consumer reads the file via `internal/audit/consumer.go:131` which delegates to the notarizer; there is no explicit owner check in code. The compose mount `/etc/tack:/etc/tack:ro` at `docker-compose.yml:301` is read-only, so any UID with read permission works. | Soften the requirement to "the file must be readable by the audit-consumer container's UID (root by default)". |

---

## 4. Drift caused by the moratorium

The two new rules (no new shell scripts, no source rsync to production)
hit five distinct steps in the runbook. Each is listed below with the
eventual replacement once the consolidation plan lands.

| Runbook step | What it does today | Why it violates a rule | Replacement once consolidation lands |
|---|---|---|---|
| Section 1 pre-flight: `test -x scripts/audit-parity.sh` | Asserts a shell script is present and executable on the operator host. | Shell-script moratorium. The file does not exist; creating it would violate the moratorium. | `go build ./cmd/server/` plus `./server ops audit help` to confirm the subcommand is registered. The dispatcher is at `internal/ops/command.go:153-165`. |
| Section 4 step 3: `rsync -az dist/tack tack:/root/tack/dist/tack-wave1` | Pushes a host-built binary to prod. | Rsync-to-prod ban. Also duplicates the deploy contract in `Makefile:123-135` which builds inside the remote. | Drop entirely. Run migrations via the freshly-built container: `docker compose run --rm --entrypoint /server app migrate`. |
| Section 5 step 1: `rsync -az --delete --exclude=... . tack:/root/tack/` | Pushes the entire source tree to prod. | Rsync-to-prod ban. | `./server ops deploy` per `incident_2026-05-09_seed_parallel_org/ops_consolidation_plan.md:1-50`. Until that lands, this step has no clean replacement; treat as a moratorium-acknowledged exception. |
| Section 5 step 5: `make deploy` | Runs `rsync -az --delete ...` then `docker build` and `docker compose up -d app` on the remote. | Rsync-to-prod ban (the `rsync` line in `Makefile:129`). | Same as above; once `./server ops deploy` lands, swap. |
| Section 6 Test 2: `bash /root/tack/scripts/audit-parity.sh --window=10m` | Runs a non-existent shell script. | Shell-script moratorium. The replacement is already merged. | `docker compose exec app /server ops audit parity` with `TACK_PARITY_FROM` and `TACK_PARITY_TO` env vars. Per `internal/ops/audit_parity.go:22-26`, the threshold defaults to 1.0 (perfect parity); set `TACK_PARITY_THRESHOLD=0.999` for a 0.1 percent tolerance during soak. |
| Section 7 hourly loop: same script | Same. | Same. | Same replacement, looped on the operator host. |
| Section 8 closeout: `make backup` | Rsyncs `scripts/backup.sh` and runs it on the remote (`Makefile:146-147`). | Shell-script moratorium covers `backup.sh`. The consolidation plan calls out `./server ops backup` as a future replacement. | Once `./server ops backup` lands, swap. Until then, document as a moratorium-acknowledged exception. |

---

## 5. Open questions for the operator

1. **Is `make deploy` a moratorium-acknowledged exception during wave 1, or
   should the runbook block until `./server ops deploy` lands?** The
   consolidation plan at
   `incident_2026-05-09_seed_parallel_org/ops_consolidation_plan.md:1-50`
   describes the future state but not the cutover date. If `./server ops
   deploy` is more than a few days out, the runbook needs a recorded
   exception so a wave 1 deploy is not blocked on the ban.
2. **Should section 4 step 3's host-built binary path be replaced with an
   in-container migrate?** The cleanest path is `docker compose run --rm
   --entrypoint /server app migrate`, but that depends on the new image
   already being built and tagged on the host. If the operator's preference
   is to migrate before the app container restarts, the order in section 5
   needs to swap (build image first, run migrate via `docker compose run`,
   then `docker compose up -d app`).
3. **What threshold should the parity scan use during the 24-hour soak?**
   The runbook is silent on this. The Go subcommand defaults to 1.0
   (perfect parity) per `internal/ops/audit_parity.go:149-159`. At Tack's
   roughly 0.37 EPS, even a single skewed event in a one-hour window drops
   the matched fraction below 1.0 and the command will exit non-zero. A
   pragmatic threshold is 0.999 or even 0.99 for the first hour; the
   operator should pick before the soak starts.
4. **Is the verb name `tack.workspace.listed` a future rename, or runbook
   drift?** If it is a rename in flight, the runbook should cite the PR.
   If it is drift, the simplest fix is to use the existing `workspace.list`
   verb from `internal/audit/verbs.go:38`.
5. **Does the audit-consumer container actually log
   `audit_consumer.started` or some other line on boot?** The runbook says
   `audit.consumer.started`. The actual emitted line is
   `audit_consumer.started` (underscore, not dot) at
   `cmd/audit-consumer/main.go:103`. Confirm whether the runbook should
   match the binary or whether the binary should be patched to use the
   `noun.verb` dot convention from `CLAUDE.md`'s logging rules.
6. **Does the runbook intend to assert ClickHouse health before declaring
   wave 1 successful?** The runbook says ClickHouse is best-effort in wave
   1 (section 10 ClickHouse-down row), but section 5 step 4 also says
   ClickHouse must report `healthy`. Pick one stance and surface it as
   either a hard gate or a soft signal.

---

## 6. Verification gates the runbook should add but does not

The runbook is missing several "did the change actually take effect?" gates
that an operator would otherwise discover only after a parity scan fails.
Each is small, but together they represent the difference between a runbook
that asserts intent and one that asserts outcome.

1. **Confirm `wrapAuditWithKafka` actually engaged on app restart.** The
   runbook says to `grep -E "audit\.recorder|audit\.kafka"` after the app
   restart (section 5 step 6). The actual log key is `audit.kafka_enabled`
   at `cmd/server/main.go:399`. Add an explicit assertion: `docker logs
   --since 5m tack-app-1 \| grep -q audit.kafka_enabled \|\| echo
   "WRAP_NOT_ENGAGED"`. Without this, an unset or typo'd
   `AUDIT_KAFKA_BROKERS` (e.g. trailing whitespace) leaves the producer in
   WAL-only mode and the operator only finds out when the smoke test in
   section 6 shows zero rows in `events_v2`.
2. **Confirm the consumer connected to Kafka.** The consumer's startup
   path emits `audit_consumer.started` at `cmd/audit-consumer/main.go:103`
   only after `audit.NewConsumer` returns without error. Add a gate:
   `docker compose logs --since 2m audit-consumer \| grep -q
   audit_consumer.started`. Without it, a misconfigured consumer DSN will
   look "running" in `docker compose ps` but will never actually consume.
3. **Confirm migration 005 ran against the wave 1 binary's expected
   schema.** Section 10's "schema mismatch" failure mode mentions this but
   the runbook never explicitly checks. Add: `docker compose exec yugabyte
   ysqlsh -U yugabyte -d tack -c "\d audit.events_v2"` and compare the
   column list against the migration source at
   `migrations/005_audit_events_v2_sibling.sql:3-21`.
4. **Confirm `audit.consumer_offsets` is being populated.** After the
   smoke test in section 6, query: `SELECT consumer_group, topic,
   partition, "offset", updated_at FROM audit.consumer_offsets ORDER BY
   updated_at DESC LIMIT 5`. If the consumer is processing but never
   committing, `events_v2` rows climb but `consumer_offsets` stays empty,
   and a consumer restart re-reads from the earliest offset.
5. **Confirm dual-write skew is sane.** The `tack_audit_dual_write_skew_seconds`
   histogram at `internal/telemetry/metrics.go:155-156` and `:218-220`
   exposes the per-event skew between Kafka and WAL acks. Add a one-time
   read of this histogram after the smoke test; a p99 above one second
   under the 0.37 EPS load is a sign the WAL is back-pressuring or the
   Kafka producer is starved.
6. **Confirm the parity command runs against the right window.** The Go
   subcommand reads `TACK_PARITY_FROM` and `TACK_PARITY_TO` from env per
   `internal/ops/audit_parity.go:22-27`. The runbook never spells out the
   window contract. Add a worked example: for the first smoke check, set
   `TACK_PARITY_FROM=$(date -u -v -10M +%FT%TZ)` and
   `TACK_PARITY_TO=$(date -u +%FT%TZ)`; for the hourly soak iteration, use
   one-hour rolling windows.
7. **Confirm `KAFKA_CLUSTER_ID` is locked in before any other service
   starts.** The compose contract at `docker-compose.yml:234` already
   refuses to start `kafka` without the var. Add a pre-start check on the
   operator side: `ssh tack 'grep -q "^KAFKA_CLUSTER_ID=." /root/tack/.env
   \|\| echo MISSING'`. The trailing dot in the regex catches the case
   where the var is set to an empty string.
8. **Confirm the rollback in section 9 actually flushed in-flight Kafka
   produces.** `KafkaRecorder.Close` calls `client.Flush(flushCtx)` at
   `internal/audit/kafka_recorder.go:178`, but the rollback does
   `docker compose restart app`, which kills the container. Add a
   pre-restart `docker compose stop --timeout 15 app` to give the
   producer a chance to drain its in-flight buffer.

---

## 7. Auxiliary observations

These do not block the wave but are worth surfacing.

- The Kafka topic create command in section 5 step 3 uses
  `--partitions 256`, matching `KAFKA_NUM_PARTITIONS: "256"` at
  `docker-compose.yml:230`. Consistent.
- The Kafka image is `apache/kafka:4.2.0` at `docker-compose.yml:215`. The
  `kafka-storage.sh random-uuid` and `kafka-topics.sh` scripts ship in
  Apache Kafka 4.x KRaft images and are present at `/opt/kafka/bin/`. I
  did not have a sandboxed network to fetch the Apache Kafka 4.2 release
  notes, so this claim is "consistent with the long-standing layout"
  rather than "verified against an authoritative release page". Operator
  should confirm against the Apache Kafka 4.2.0 docs or a local
  `docker run --rm apache/kafka:4.2.0 ls /opt/kafka/bin/` before relying
  on the exact path.
- The runbook says `audit-consumer` "stays idle until events arrive"
  (section 5 step 5). This is consistent with the consumer's franz-go
  poll loop in `internal/audit/consumer.go`, but the consumer also runs
  the embedded notarizer goroutine on a 60s tick by default
  (`AUDIT_CONSUMER_NOTARIZER_PERIOD` at `cmd/audit-consumer/main.go:31`).
  An idle consumer still exercises that path; expect occasional
  `audit.consumer.notarizer_failed` lines if the signing key is missing.
- Section 5 step 3's `--config min.insync.replicas=1` flag is redundant
  with `KAFKA_MIN_INSYNC_REPLICAS: "1"` at `docker-compose.yml:232`, and
  `--config unclean.leader.election.enable=false` is redundant with
  `KAFKA_UNCLEAN_LEADER_ELECTION_ENABLE: "false"` at
  `docker-compose.yml:233`. Setting them at topic-create time pins the
  topic-level config so a future broker default change does not silently
  flip them. Consistent with the runbook's stated intent.
- The runbook's section 10 ClickHouse-down row says "wave 1 parity gate
  looks at Yugabyte only". The Go parity scanner at
  `internal/ops/audit_parity.go:65-96` indeed reads from
  `cfg.AuditReaderDSN` or `cfg.AuditWriterDSN`, both of which point at
  Yugabyte. ClickHouse is not part of the parity gate. Consistent.
