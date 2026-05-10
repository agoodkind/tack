# Post-Incident Roadmap

**Date drafted:** 2026-05-09 during execution subagent run
**Last updated:** 2026-05-09 22:38 UTC
**Purpose:** Sequence of work after the incident's forward-fix completes

This document captures the work that should happen after the execution subagent
returns a "success" verdict in `execution_report.md`. The order below is the
recommended order. Prerequisites are explicit per item.

## Status snapshot

- Forward-fix: done. MCP works, parallel org cleaned, address index repointed.
- Phase 1 (audit drop fix): **deployed** as commit `23ad44a` at 22:32 UTC. Read-class audit events landing again. Smoke verified end-to-end.
- Phase 2 (Kafka horizontal design): wave 1 build preserved at `/Users/agoodkind/Sites/tack-phase2-wave1-wip`; integration pending.
- Backup script rebuild: in progress (re-spin agent running).
- Backup restore-test infrastructure: built at `/Users/agoodkind/Sites/tack-backup-restore-test`; merge to main pending.
- Tier 1 follow-ups still open: Temporal-DB backup gap (covered by rebuild), QA environment, address-index design decision, stale API token cleanup.

---

## Phase A: Confirm incident closure

Owner: operator + Claude.
Prerequisite: execution subagent reports verdict=success.

1. Read `execution_report.md`. Verify:
   - All 7 phases completed.
   - Each MCP validation in Phase 7 passed.
   - Three snapshots taken and offsite copies verified.
   - `backup_agent` sidecar was torn down.
2. Independent re-validation by operator. Run from operator's MCP client:
   - `tack_describe_workspace {"workspace_reference":"main"}` returns OLD workspace UUID.
   - `tack_list_projects {"workspace_reference":"main"}` returns 7 projects.
   - `tack_get_project {"workspace_reference":"main","node_id":"TACK"}` succeeds.
3. Spot check at least one issue under one project to ensure deeper resolver paths work too.
4. Update retro log:
   - Section 1 incident summary updated with resolution.
   - Section 8.5 outcome updated.
   - Section 9 outstanding work pruned.
   - Add a section 11 "resolution" with timeline of recovery.
5. Mark incident closed in any tracking system.

Exit criteria: production MCP works for the operator's normal workflow,
the retro reflects resolution, the operator declares closure.

---

## Phase B: Resume the original migration plan

Owner: operator + Claude.
Prerequisite: Phase A complete.

The original plan at
`/Users/agoodkind/.cursor/plans/finish-slug-and-state_74920a00.plan.md`
had 8 sections. Section 1 through 4 were partially executed; the rest are
still ahead. Status reset based on incident resolution:

- Section 1 (Stabilize the code baseline): code on `main` at `dd430c9`. Done.
- Section 2 (Prepare safe deployment source): clean source at
  `/Users/agoodkind/Sites/tack-deploy-source-20260509T041358Z` was used.
  Done. Future deploys still need a clean source.
- Section 3 (Deploy wave 1): done. Verified post-incident.
- Section 4 (Address backfill): preview blocked by 2 conflicts during the
  incident. Apply was run as Phase 6 of the forward-fix and populated the
  legacy slug entries. Re-run preview post-recovery to confirm
  `conflict_count=0`. If clean, this section is done.
- Section 5 (Validate post-backfill reference behavior): partially covered
  by Phase 7 of the forward-fix. Add the additional validations the
  original plan called out: list/filter, search, generated state setters,
  epic and state references.
- Section 6 (Finish the remaining incident state repair): completely
  separate from today's incident. The 2026-05-08 state repair has open
  policy buckets. Resume as planned.
- Section 7 (Close the slug refactor: legacy surface removal): code
  cleanup. Wait until Section 6 completes.
- Section 8 (Final production closeout): final disposition documentation.

Recommended order:

1. Re-run `./server ops batch backfill.addresses.preview`. Confirm
   `conflict_count=0` and `write_count` reflects only no-op or expected
   work. If clean, mark Section 4 done.
2. Run the additional validation calls that Section 5 lists but the
   forward-fix Phase 7 did not cover.
3. Move on to Section 6 (the 2026-05-08 incident state repair). This
   requires three policy decisions noted in the plan:
   - 4 unresolved `status` rows: which default state.
   - 2 `progress;status` rows: legacy `progress` preservation policy.
   - 1 `status` row: live-value confirmation before exact-match removal.
4. Run remaining repair preview/apply passes per the plan.
5. Produce final residual inventory.
6. Section 7: code cleanup. Remove legacy `slug_index` inspection,
   `TACK_REPAIR_NODE_ID` fallback, slug-only matchers and wording. Re-run
   `make lint-diff`, `make test`, `make build`.
7. Wave 2 deploy from a clean source.
8. Section 8: final closeout.

Exit criteria: all original plan sections complete, residual inventory
empty or explicitly accepted, wave 2 deployed and verified.

---

## Phase C: Operational follow-ups (parallel with Phase B)

Owner: operator (plus Claude where useful).
Prerequisite: Phase A complete. Can run in parallel with Phase B.

These are the items the incident surfaced that are not part of the
original migration plan but cannot wait until after closeout. Listed by
risk-tier.

### Tier 1: backup system rebuild (must happen soon)

The empty-backup defect is an active operational risk. Until resolved,
any FDB-affecting incident is unrecoverable except by manual
reconstruction.

1. Replace `scripts/backup.sh` with a real FDB backup mechanism.
   Options:
   - `fdbbackup` with a persistent `backup_agent` Compose service.
   - `fdbbackup` driven from a sidecar that comes up only during backup
     runs (the incident proved this works).
   - Override the image's `VOLUME` declaration in `docker-compose.yml`
     so the named volume actually holds the data, then keep using
     volume tar (still has live-tar consistency caveat, so not preferred).
2. Apply the same scrutiny to Yugabyte. Replace live-tar with
   `pg_dump` or scheduled YSQL backup. Keep the CSV dumps for
   `users`, `api_tokens`, and `org_members` as authoritative auth
   backups.
3. Add a CI test that fails when a backup tarball does not contain the
   expected file types after a dry-run backup.
4. Add a verification cadence (suggest monthly): test-restore the
   latest backup into a scratch FDB cluster and confirm health.
5. Audit and correct any docs or runbooks that reference the existing
   script as a recovery resource.

### Tier 2: seed transition-state safety

The seed planning gap allowed today's incident. Closing it before the
next migration is required.

1. Make seed consult both `address_index` and `slug_index` during a
   transition window. Decide what the transition signal is (env var,
   config flag, or implicit "if any `slug_index` entries exist").
2. Add a deploy-time precheck that compares `slug_index` and
   `address_index` populations and refuses to deploy a binary whose
   reference-strategy metadata has changed if the legacy index still has
   entries.
3. Add a smoke test between deploy, seed, and backfill that performs
   one read-only MCP resolution against a known production reference
   and aborts the migration if it fails.

### Tier 3: address index design decision

The global-vs-org-scoped address index inconsistency is captured in
retro section 1B. It does not block today's recovery, but it blocks
multi-tenant scaling.

1. Decide the target design: global, fully scoped, or hybrid (org
   global, everything else scoped). Capture in an ADR.
2. Update CLAUDE.md to match the decision.
3. If the target is scoped or hybrid, plan a migration of existing
   `address_index` rows. Apply lessons from this retro to that plan.
4. Implement or remove the `node_address_by_node` reverse index per
   the decision.
5. Add a CI consistency check that fails when CLAUDE.md FDB key shapes
   drift from key constructors in
   `internal/adapters/foundationdb/keys.go`.

### Tier 4: token rotation

Seed printed a production token to stdout during the incident.

1. Rotate the production API token.
2. Redact or rotate any operational logs that captured the token.
3. Update any clients (Claude Code MCP config, etc.) with the new
   token.

### Tier 1.5: QA environment (parallel with backup rebuild)

Tier-tagged 1.5 because it shares Tier 1's "must happen before next
migration" urgency, but it can run in parallel with the backup rebuild
since the rebuilt backup is the data source for QA refreshes.

The incident exposed that there is no out-of-band environment in which
to exercise migrations, seed runs, backfills, or backup restores before
production. See retro section 1C for the full case.

1. Stand up a QA host or container stack matching production
   configuration. Scope (CT, VM, or scratch cluster pool) to be
   decided based on resource availability.
2. Build a repeatable production-to-QA data refresh procedure that
   consumes output from the rebuilt backup system.
3. Add QA to the deploy pipeline as a required stop before production
   for any change that affects schema, FDB key shapes, seed behavior,
   or migration paths.
4. Add the QA-validation step as a precondition in every migration
   wave's stop conditions.
5. Decide how to seed the second-tenant case in QA so the address
   index work in retro section 1B can be validated there before any
   production migration.

---

## Decision log

This roadmap is a recommendation, not a contract. The operator decides
the order, scope, and tempo of each item. The incident's most urgent
items are Phase A (closure) and Phase C Tier 1 (backups). Everything
else can be sequenced based on operator priority.
