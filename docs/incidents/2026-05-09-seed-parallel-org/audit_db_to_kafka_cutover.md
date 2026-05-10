# Audit DB-to-Kafka cutover

**Originally:** `wave2_cutover_plan.md`, drafted 2026-05-09 with a gradual rollout.
**Now:** rewritten 2026-05-10 to match the hard-cutover decision the operator approved.
**Tracks:** TACK-232 in the TACK-13 audit-migration epic.
**Pre-requisite:** TACK-231 (Phase 2 wave 1 production deploy) must have shipped and run in dual-write for long enough that the operator has confidence in the consumer's projection.

---

## 1. What this is

This is the plan for the moment Tack's audit subsystem stops writing directly to YugabyteDB and starts writing only to Apache Kafka. Before the cutover, the audit producer fans every event to both YugabyteDB and Kafka, and the consumer reads the Kafka stream into a sibling table called `audit.events_v2`. After the cutover, the producer writes only to Kafka, the consumer is the only thing inserting into the canonical `audit.events` table, and the sibling table has been renamed in place to take over the canonical name.

The migration is a hard cutover with table renames. There is no gradual rollout, no soak window between dual-write and Kafka-only, and no router-mode env var. The operator's reasoning is that Tack has no external clients yet, so a brief audit-availability gap during the renames is acceptable, and a hard cutover is dramatically simpler operationally than the alternatives. If anything fails the smoke test at the end, the migration reverses the renames and the system is back to dual-write.

---

## 2. The decision (hard cutover, not gradual)

The original wave 2 plan described three options: Option A had the producer go Kafka-only with the consumer as the sole writer, Option B kept dual-write indefinitely with the WAL acting as a fallback, and Option C kept dual-write forever and just renamed the sibling table. The plan recommended Option A with a parity gate, an `AUDIT_RECORDER_MODE` env var as a rollback knob, and a 7-day soak. The operator dropped that machinery and chose a fourth shape: do Option A as a hard cutover with rename and smoke tests, no soak, no env-var router.

The reasoning was specific. Tack has no external clients in production today, so an audit-availability gap measured in seconds to a couple of minutes during the rename is acceptable. The operator also pointed out that the gradual machinery's main job is to give a graceful path back to the old system if the new one misbehaves, but in our situation the rollback path is just running the renames in reverse, which is fast and well-understood. Adding the env-var router and the soak window was carrying complexity for a scenario that is not the actual constraint.

The operator was explicit that this hard-cutover shape applies once, while there are no clients. After clients exist, any equivalent migration would need to be designed for zero downtime, and the gradual approach (or something equivalent) becomes the only acceptable path.

---

## 3. The state wave 1 leaves us with

When wave 2 starts, the production environment looks like this. The `tack-app-1` server process has the dual-write recorder wired in, so every audit event goes to both YugabyteDB and Kafka. The `tack-audit-consumer-1` Compose service reads from the Kafka topic and projects into `audit.events_v2`, with offsets tracked in `audit.consumer_offsets`. The `audit.events_v2_dlq` table holds events the consumer could not process. The legacy table `audit.events` is still being written to synchronously by the producer, the same way it has been since before wave 1.

Two writers are competing for the same compliance contract during this window. The legacy synchronous path at `internal/audit/yugabyte.go:200` advances `audit.chain_heads` in the same transaction as the row insert. The consumer's `advanceChain` at `internal/audit/consumer.go:630` does the same thing for events it pulls from Kafka. Both extend the hash chain for `(org_id, shard)`. The dual-write design accepts this because the chain extensions are commutative and idempotent at the row level, but it is the central problem wave 2 has to resolve. After cutover, only the consumer is allowed to extend the chain.

The wave 1 migrations that landed are 003 (`audit.consumer_offsets` and `audit.events_event_id_uniq`) and 005 (`audit.events_v2` plus `audit.events_v2_dlq`). Migration 004 added a uniqueness constraint to `event_id` to make the dual-write path idempotent on retry. None of these touch `audit.events` directly, so the legacy table is unchanged from before wave 1.

---

## 4. The cutover sequence

The migration is a single new SQL migration plus a coordinated app-restart sequence. The end state is that `audit.events_v2` becomes `audit.events`, the old `audit.events` is archived, and the producer is in Kafka-only mode.

**Step 1: take a verified backup.** Run `./server ops backup` from the operator's Mac. After it completes, run `./server ops backup verify <path>` against the new artifact. Both must succeed before the cutover proceeds. If `make build` is still failing on the deploy or backup tooling, the cutover does not start; we do not run a hard cutover from a tree that cannot build.

**Step 2: stop the producer briefly.** This is a graceful shutdown of the app: `docker compose stop --timeout 15 app` on the production host. The 15-second timeout is enough for the Kafka producer to drain its in-flight batch (the producer's drain semantics live at `internal/audit/kafka_recorder.go:178`). The consumer is left running so it can finish projecting any events still in Kafka. Audit-availability is now zero for state-change verbs, because nothing is producing them. Read-class verbs are also zero because the WAL is still drained.

**Step 3: run migration 006 (the rename migration).** The migration does four things in one transaction: it renames `audit.events` to `audit.events_legacy_archive`, it renames `audit.events_v2` to `audit.events`, it renames `audit.events_v2_dlq` to `audit.events_dlq`, and it updates the consumer's projection target string in `audit.consumer_offsets` so the consumer keeps committing into the same row after the rename. Postgres-on-Yugabyte handles renames transactionally, so either all four happen or none of them do. The legacy archive table is kept rather than dropped so that wave 2's smoke test has a comparison target if anything looks off.

**Step 4: restart the consumer.** `docker compose restart audit-consumer` on the production host. The consumer reads `audit.consumer_offsets` to find its commit position, opens the Kafka topic from there, and starts projecting into the renamed `audit.events`. There is a brief window where the consumer is catching up on whatever events landed in Kafka while the app was stopped; the smoke test below covers that case.

**Step 5: restart the producer in Kafka-only mode.** Update the `tack-app-1` env so `AUDIT_RECORDER_MODE=kafka` (or whatever the final env-var name is in the deploy commit), then `docker compose up -d app` to restart with the new env. The producer now writes only to Kafka. The legacy synchronous Yugabyte path at `internal/audit/yugabyte.go:200` is bypassed because the dual-writer wrapper is no longer in the chain.

**Step 6: smoke test.** Run five MCP calls from the operator's Mac through the production endpoint: `tack_list_workspaces`, `tack_describe_workspace`, `tack_list_projects`, `tack_get_project`, and a state-change verb like creating and immediately deleting a throwaway test issue. After each call, check that the corresponding row appeared in the renamed `audit.events` within a few seconds. If all five appear with the right verbs, the cutover is successful.

**Step 7: declare cutover done.** Update the TACK-232 ticket with the cutover timestamp, the smoke test results, and a note that `audit.events_legacy_archive` is now safe to drop on the operator's preferred schedule (no rush; it is read-only and small relative to the event volume from this point forward).

---

## 5. Rollback procedure

If anything fails during steps 3 through 6, the rollback is fast because it is just reversing the renames and putting the producer back in dual-write mode.

**If migration 006 fails to apply.** The transaction rolls back. The tables are unchanged. The producer is offline. Restart the producer in dual-write mode (`AUDIT_RECORDER_MODE=dual` or unset, depending on the deploy commit's default). The system is back to wave 1 state. Investigate the migration failure offline; do not retry the cutover until the cause is understood.

**If migration 006 applies but the consumer fails to restart cleanly.** The renames are committed, so the canonical name `audit.events` now points at what was `audit.events_v2`. The fastest path back is to start a new migration 007 that reverses the renames. The producer stays offline during this window. After migration 007 commits, restart the producer in dual-write mode pointed at the original `audit.events`. The newly-projected rows that the consumer wrote to the renamed table during the brief window are preserved as `audit.events_v2` again and the operator can decide whether to merge them in offline.

**If the smoke test fails after step 5.** Same path as the consumer-failed case: run a reverse-rename migration, restart the producer in dual-write mode. The diagnostic question is "did the producer reach Kafka, and did the consumer commit the offset?". Both halves of that question are answerable from the producer's `kafka.produce.failed` slog events at `internal/audit/kafka_recorder.go:130` and the consumer's `consumer.lag.high` and `consumer.processed` metrics.

**The rollback budget is short.** If the cutover does not produce a clean smoke test within roughly 30 minutes of the producer restart in step 5, abort and reverse. Do not let the system sit in a half-cutover state hoping the issue resolves itself. The ability to roll back fast is what makes the hard cutover acceptable; spending hours debugging in place defeats the entire shape.

---

## 6. State-change verbs are unaffected

State-change verbs (`node.create`, `node.update`, `node.delete`, anything mutating) bypass the WAL and write synchronously to YugabyteDB through the inner recorder. After cutover, they still bypass the WAL, but the path that previously wrote synchronously to YugabyteDB now writes synchronously to Kafka instead. The user's MCP request still blocks until the audit event is durable somewhere; the somewhere just changed.

This is load-bearing for compliance. State-change events must be durable before the user gets told their request succeeded, because the user just changed something and the audit record of that change must exist before they hear "ok". Kafka with `acks=all` and replication factor 1 (single-broker today) provides that durability the same way Yugabyte did. When the cluster grows past one broker (deferred to wave 3), the replication factor goes up and the durability story strengthens.

The hash chain extension for state-change verbs becomes the consumer's responsibility after cutover, the same way it is for read-class verbs. The compliance contract on the chain seal is "eventual": the row landing durably in Kafka is sufficient for the user request to return success, and the chain extension runs behind that on the consumer's commit cadence. The operator approved this interpretation on 2026-05-09. If it ever needs to be tightened to "synchronous chain seal before return", the cutover plan splits into two waves and a third-party signing or attestation layer becomes part of the architecture.

---

## 7. Schema migration: rename, not drop or copy

The cutover migration renames tables in place. It does not copy rows from `audit.events_v2` into `audit.events`, and it does not drop `audit.events`.

**Why rename.** The new home for canonical audit data is the table that the consumer is already writing to. That table is `audit.events_v2`. Every reader of audit data (MCP audit query tools, the notarizer, compliance dashboards) currently reads from `audit.events`. The migration's job is to make `audit.events` point at the v2 table so readers do not have to change. Renaming achieves that with one DDL statement per table, no data movement, no row count to watch, no integrity checks. Postgres-on-Yugabyte commits all four renames atomically, so readers either see the old layout or the new layout, never a mix.

**Why not drop.** Dropping the legacy `audit.events` would lose the historical audit data that accumulated before wave 1 and during the dual-write window's exclusively-legacy verbs (some state-change verbs landed in legacy before they landed in v2). The legacy table is renamed to `audit.events_legacy_archive` instead. The operator can decide later whether to keep it as a permanent archive, copy its rows into the new `audit.events` to consolidate history, or drop it once the dual-write window's coverage is verified.

**Why not copy.** Copying every row from v2 to a fresh canonical table would be slow (potentially hours, depending on the dual-write window's accumulated volume) and would require a downtime window proportional to the data size. Renaming is constant-time. The downside of renaming is that the canonical table now has a different physical home in the underlying storage; for a brief period after the rename, query plans that were tuned to the old physical layout might be off. This has not been a real problem in Postgres workloads of this shape and we do not expect it to be one here.

The migration file is `migrations/006_audit_kafka_cutover.sql`. It is the only schema change in the cutover sequence.

---

## 8. Verification gates

The cutover has three gate types: pre-flight (must pass before step 1), during-deploy (must pass at each step), and post-deploy (must pass during the first hour after step 6).

**Pre-flight gates.** The deploy and backup tooling must build clean (`make build` exits zero). The most recent backup must verify clean (`./server ops backup verify <path>` exits zero). The wave 1 deploy must have been live in dual-write mode for at least 24 hours with no `audit.consumer.stalled` or `kafka.produce.failed` events more frequent than once per hour. The legacy `audit.events` and the v2 `audit.events_v2` tables must have row counts that are within 1 percent of each other for the most recent hour, which is the lightweight parity check that replaces the original 7-day soak.

**During-deploy gates.** Each step in section 4 has its own pass condition documented in line. The most important is step 6's smoke test: five MCP calls produce five rows with matching verbs in the renamed `audit.events`. If any row is missing, the cutover does not move on and rollback starts.

**Post-deploy gates.** During the first hour after step 6, watch four metrics. The producer's `tack_audit_kafka_produce_total{result="error"}` counter should not increase. The consumer's `tack_audit_consumer_lag_seconds` should stay under 30 seconds for the system org and under 5 seconds for the user-facing orgs. The DLQ row count in `audit.events_dlq` should not increase. The hash chain head in `audit.chain_heads` should advance at the same per-org cadence the notarizer signs on (one extension per minute per org under normal load). If any of these go off, the rollback budget is the same 30 minutes that step 5's smoke test budget was; do not debug in place past that.

---

## 9. Backup coverage updates

The cutover changes the table list that backup-content-check needs to know about. Today's hardcoded list at `scripts/backup-content-check.sh:206` covers `events`, `chain_heads`, `notarizations`, and `pii`. After wave 1 deploys, that script also needs to verify `audit.events_v2` and `audit.consumer_offsets` are present. After the cutover migration 006 runs, the list shifts again because `audit.events_v2` has been renamed to `audit.events` and `audit.events_v2_dlq` has been renamed to `audit.events_dlq`.

The hardcoded approach does not scale across this many renames. TACK-236 (extend backup content-check coverage) is the ticket that makes the list configurable so the verify script reads what schema is actually present rather than asserting against a hardcoded snapshot. TACK-236 should land before the cutover so the verify after migration 006 keeps working without an emergency edit. If TACK-236 has not landed in time, the cutover deploy includes a one-line patch to `backup-content-check.sh` that points at the new table names; this is a violation of the no-shell-edits rule and is acknowledged as a known cost of running ahead of TACK-236.

The Yugabyte logical dump (the `ysql_dump` step in the backup script) covers the entire `audit` schema with `--schema=audit`, so it captures whatever tables exist at backup time, regardless of names. The rename does not break the dump.

---

## 10. What the operator must answer before cutover starts

Five questions, distilled from the original twelve in the gradual-rollout plan.

The first must be answered before the deploy starts because it changes the cutover's shape. The compliance contract on the audit hash chain: does it need to seal synchronously before a state-change MCP call returns success to the user? The operator's standing answer is "eventual", which is what the cutover plan above assumes. If that answer ever changes to synchronous, the cutover splits into two waves and the consumer's role changes.

The second is whether any verb currently classified as read-class needs to be reclassified as state-change for compliance reasons. The classification table is at `internal/audit/verbs.go:63-82`. After cutover, read-class verbs go through Kafka with eventual chain extension; state-change verbs go through Kafka but with synchronous-to-Kafka durability before the user request returns. If a verb is in the wrong bucket, today is the time to move it.

The third is the policy for reviewing and clearing `audit.events_dlq`. The DLQ exists so the consumer never crashes or silently drops, but any row sitting in it is an event that did not reach the canonical table. A written cadence (daily, weekly, etc.) and a written workflow for what an operator does when a row appears must exist before cutover so the post-deploy gates have a target. The replay tool itself is TACK-240 and is not a cutover blocker, but the manual policy needs to exist.

The fourth is the cleanup schedule for `audit.events_legacy_archive`. Three options: keep it forever as a permanent archive (cheap, takes disk), drop it after some retention window (operator decides the window), or merge its rows into the new `audit.events` to consolidate history (one-time copy job, then drop). The cutover does not require a decision before it runs because the archive table is read-only and not in any hot path; this question can be deferred until the cutover is verified clean.

The fifth is the smoke-test scope. The cutover plan above proposes five MCP calls. The operator may want a different test set, a different number of calls, or a synthetic load run instead. This is just a definition of done for step 6 and is small to adjust.

---

## 11. What lands in code

The cutover ships these artifacts.

A new SQL migration `migrations/006_audit_kafka_cutover.sql` that does the four renames atomically. The migration also includes a `BEGIN`/`COMMIT` block, an explicit lock on `audit.events` and `audit.events_v2` to serialize concurrent writers (there should not be any during step 2's stopped-producer window, but the lock makes that guarantee explicit), and a final consistency check that asserts `audit.events.event_id` has the expected uniqueness constraint after the rename.

An env-var change in `internal/config/config.go` for `AUDIT_RECORDER_MODE` that supports at minimum `dual` (current default), `kafka` (post-cutover default), and `wal` (pre-Kafka legacy mode kept for emergency fallback). The router lives in the producer wrapper at `internal/audit/dual.go` or its successor; the exact file depends on whether wave 1's `dual.go` survived intact or was refactored.

An update to `cmd/audit-consumer/main.go` (no change in shape, just confirming the consumer reads its projection target from `audit.consumer_offsets` rather than a hardcoded name; the wave 1 plan flagged this at `internal/audit/consumer.go:605` as a string literal that needs to read from config or from the offsets row).

A revision of `docs/phase2-wave1-runbook.md` (or a new `docs/audit-cutover-runbook.md`) that walks the operator through steps 1 through 7 with the actual commands. The runbook should reference this plan document for context but be self-contained as an operations script.

---

## 12. Where this leaves the rest of the audit roadmap

After the cutover, the audit subsystem looks like this. The producer writes only to Kafka. The consumer is the only writer to `audit.events` and the only extender of `audit.chain_heads`. The legacy synchronous Yugabyte path at `internal/audit/yugabyte.go:200` is dead code that can be deleted in a follow-up cleanup. The dual-writer wrapper at `internal/audit/dual.go` is also dead code, except possibly for the `wal` fallback mode if the operator wants to keep it as an emergency lever.

Phase 2 wave 3 (consumer scale) and wave 4 (cold archive) become possible after the cutover because the architecture is now actually horizontal. The producer talks to Kafka on the wire, and Kafka's protocol is the same whether there is one broker or many. Adding brokers is a config change rather than a code change, and the consumer's projection-into-Yugabyte step is the bottleneck that wave 3 addresses by running multiple consumer instances, each owning a partition range.

The OpenTelemetry migration (TACK-227) is sequenced after the cutover lands and ideally before wave 4, so the new metrics shape is in place before another architecture-changing wave. Today's `expvar` metrics emulate histograms with paired ints and synthesize labels as colon-joined strings; OpenTelemetry models both natively and integrates with traces and logs through one SDK.

---

## 13. End

This document supersedes `wave2_cutover_plan.md`. The original document remains in git history if the gradual-rollout context is ever needed. The TACK-232 ticket description points here.
