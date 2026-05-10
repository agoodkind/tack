# Phase 2 Wave 1 Runbook Report

Companion note for `docs/phase2-wave1-runbook.md`. Lists what the
runbook covers, what it punts to other docs, and the open questions
the operator should be aware of.

## What the runbook covers

- Pre-flight gate: phase 1 commit `23ad44a` precondition, 7-day clean
  wave-0 soak, `make backup` producing a non-empty `MANIFEST.txt`,
  audit tests green on the wave 1 branch, the four new compose
  services present, and `scripts/audit-parity.sh` executable.
- One-time `KAFKA_CLUSTER_ID` generation via `docker run --rm
  apache/kafka:4.2.0 /opt/kafka/bin/kafka-storage.sh random-uuid`,
  persisted in `/root/tack/.env`, and the verification command that
  reads `meta.properties` from the running broker.
- The full env var matrix for wave 1 with values, including the
  `AUDIT_KAFKA_BROKERS` rollback semantics. The wave-0-on-empty
  behavior is cited against `cmd/server/main.go` `wrapAuditWithKafka`,
  which is the actual code path.
- Migration order: 003, 004, 005 BEFORE the new app binary takes
  traffic, run via `./server migrate`, with a post-run schema check.
- Deploy sequence: pull images, bring up Kafka first (so it formats on
  the pinned cluster ID), explicit `kafka-topics.sh --create` for
  `audit.events.v1` with 256 partitions and RF=1, then SeaweedFS and
  ClickHouse, then `audit-consumer`, then app restart.
- Smoke test: one MCP `tack_list_workspaces` call plus a verification
  query on both `audit.events` and `audit.events_v2`, followed by
  `scripts/audit-parity.sh --window=10m`.
- 24-hour parity gate with hourly script runs in a loop, plus producer
  failure-counter and consumer-lag checks.
- Wave 1 exit criteria: 24 hours of zero drift, lag p99 under 5 s,
  zero `audit.kafka.produce_failed` warns, zero
  `audit.consumer.stalled` errors, and a closing `make backup` that
  captures the new tables.
- Rollback procedure: clear `AUDIT_KAFKA_BROKERS`, restart app, stop
  consumer, optional `audit.events_v2` drop, image-tag rollback path.
- Failure mode table for broker down, consumer behind, ClickHouse
  down, and schema mismatch, plus two extras (offset corruption and
  notarizer key missing).

## What the runbook punts

- Wave 2 cutover steps: future `docs/phase2-wave2-runbook.md`.
- ClickHouse OLAP read-path wiring: wave 3.
- Iceberg/SeaweedFS cold archive: wave 4.
- N=many migration: design doc section 13.
- Backup-and-restore drills for `audit.events_v2` and
  `audit.consumer_offsets` beyond the closing-soak `make backup` check
  in section 8: `docs/backup-runbook.md` is treated as authoritative
  there. The runbook does not extend it.
- Detailed metric dashboard layouts. The runbook names the metrics
  (`audit.kafka.produce_failed_total`, consumer-group LAG) but does
  not specify a Grafana panel layout.

## Open questions

These are real uncertainties the operator may hit; they are listed
here rather than buried in the runbook so they can be resolved by the
team before the first deploy.

- The `AUDIT_KAFKA_BROKERS` env wiring is not yet present in
  `.env.example` even though the producer report claims it was added.
  The runbook tells the operator to write the values directly into
  `/root/tack/.env`, which is correct; the example file should still
  be updated for future consistency. Tracked separately.
- The compose file checked into `phase2-wave1-rebase` does not yet
  contain `kafka`, `seaweedfs`, `clickhouse`, or `audit-consumer`
  service blocks. The producer and consumer implementation reports
  describe adding them, but a fresh `grep` against `docker-compose.yml`
  returns no matches. The runbook gates pre-flight on those blocks
  being present; if a follow-up commit lands them out of order, the
  pre-flight check will catch it.
- The runbook references `scripts/audit-parity.sh` as an external
  artifact built by a parallel worktree. That script is NOT in the
  current tree (`ls scripts/audit-parity.sh` returns no such file).
  The pre-flight check covers this, but operators reading the runbook
  before the script lands will hit a hard stop.
- The MCP smoke-test curl uses `TACK_DEV_TOKEN` as a placeholder for
  the auth token env var. Production may use a different name; the
  runbook flags this inline with a parenthetical.
- The runbook claims migrations 003-005 are forward-only and
  idempotent. Migration 004 has a guarded pre-check that aborts on
  pre-existing duplicate `event_id` values; that is documented. The
  idempotency claim for 003 and 005 is based on the
  `wave1_consumer_implementation_report.md` description; the actual
  SQL was not re-read line by line during this writeup, so an
  operator who hits a non-idempotency surprise should check the goose
  status table first.
- The design doc section 12.1 wave 1 originally numbered the
  migrations 005 and 006. The repo numbers them 003, 004, and 005.
  The runbook follows the repo. If a future commit re-numbers them,
  the runbook needs to be updated in lockstep.
- The `KAFKA_CLUSTER_ID` generation command is the design-doc
  command and is the standard `kafka-storage.sh` invocation, but the
  runbook explicitly notes "verify against the broker image's docs
  before running" for the topic-create flags because some 4.x flag
  defaults shift release-to-release.

## Honesty notes

- The runbook does not invent a `docs/phase2-wave2-runbook.md` link;
  it labels that file as future work.
- Wave 1's compose-tag rollback path assumes a `:previous` tag exists
  on the host. The runbook says so explicitly rather than guaranteeing
  it.
- The 5-second p99 lag target is taken from design doc section 12.1's
  wave 1 description; the runbook flags that this number should be
  revisited if production EPS climbs above 100.
- Length: 497 lines, inside the 250-500 target.
