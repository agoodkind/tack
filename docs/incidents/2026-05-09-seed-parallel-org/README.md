# Incident 2026-05-09: Seed-Created Parallel Org

This directory holds the documentation produced during and after the 2026-05-09 production incident on Tack (CT 117). The directory was relocated and trimmed on 2026-05-10: it used to live at the repo root as `incident_2026-05-09_seed_parallel_org/`, and it now lives under `docs/incidents/` so the incident sits alongside other tracked documentation. A number of stale plans, transient revision notes, post-completion status files, and sensitive binary artifacts were removed at the same time. What remains here is the load-bearing narrative plus the agent run reports as audit trail.

## What happened, in one paragraph

The session started as a Phase 2 wave 1 deploy and ran a `seed` pass against production. Seed checked the new generic `address_index` FDB family for the existing org and workspace, did not find them because the legacy data still lived under the predecessor `slug_index` family that nobody had backfilled yet, and created parallel UUIDs for `goodkind-io` and `main`. From that moment until the forward-fix, MCP returned a new empty workspace for `workspace_reference: "main"` while the real production projects, issues, and epics continued to live under the legacy UUIDs in a part of FDB that nothing was reading. Recovery had to be forward-fix because the production FDB backups had been silently empty for two weeks (a Docker anonymous-volume shadowing issue). Two additional findings surfaced during recovery: the audit-WAL had been silently dropping read-class events for over eleven hours due to an idle-segment-not-rotating bug (Phase 1 fix shipped at commit `23ad44a`), and the architecture had drift in the address-index family and in tenant-isolating ID derivation that needed broader cleanup.

## Start here (for the next agent)

The single most important document in this directory is **[HANDOFF.md](HANDOFF.md)**. It is the cold-read briefing for whoever picks up the work next. It defines every term inline, names every active ticket and its status, lists the operator-imposed rules, names the worktrees and their states, and tells the next agent exactly which subagent to spawn first (TACK-228, the lint pass that unblocks `make build` on the new Go backup tooling).

If you are the next agent, read HANDOFF.md and then go directly to TACK-228. Everything else in this directory is supporting material.

## Plans and decisions (top level)

These are the forward-looking documents and the design rationale.

- **[HANDOFF.md](HANDOFF.md).** The handoff briefing.
- **[retro_log.md](retro_log.md).** The live retrospective. Sections 1A through 1H capture the architectural and operational findings; the rest captures the timeline, root cause, decision points, resolution, and references.
- **[post_incident_roadmap.md](post_incident_roadmap.md).** Tier 1 through 4 follow-up roadmap. Partially superseded by the Tack issues filed under epics TACK-12 through TACK-16; the roadmap remains useful as the narrative version of what those tickets are for.
- **[ops_consolidation_plan.md](ops_consolidation_plan.md).** Designs the move of backup, verify, restore-test, and deploy from shell scripts into Go subcommands under `./server ops`. Three deliverables. Treats deploy as image-based with `docker save | ssh | docker load`, no container registry.
- **[audit_two_phase_plan.md](audit_two_phase_plan.md).** Phase 1 (WAL idle-rotation fix, shipped at commit `23ad44a`) and Phase 2 (the Kafka migration) plan.
- **[audit_horizontal_design.md](audit_horizontal_design.md).** The Phase 2 horizontal-from-day-one architecture: Apache Kafka producer, ClickHouse OLAP read path, Yugabyte integrity tables, Iceberg cold archive on SeaweedFS or Garage. Five waves from N=1 to N=many.
- **[audit_scale_architectures.md](audit_scale_architectures.md).** Research that backs the Kafka decision. No public system delivers hash chain plus signing plus PII plus 1M+ EPS together; Sigstore Rekor is the closest analogue at about 2 to 3k EPS per shard.
- **[audit_db_to_kafka_cutover.md](audit_db_to_kafka_cutover.md).** The wave 2 cutover plan. Originally drafted as `wave2_cutover_plan.md` with a gradual rollout; rewritten on 2026-05-10 around the operator-approved hard cutover with rename and smoke tests. The renamed document is the authoritative wave 2 plan.
- **[address_index_design_decision.md](address_index_design_decision.md).** The design exploration that the operator pushed back on. The agent recommended Option C (hybrid scoping); the operator chose to delete the redundant `address_index` family entirely under TACK-229 and route address lookups through the existing org-scoped `node_by_property` index. Read this document for context, but do not act on its Option C recommendation.
- **[remediation_playbook.md](remediation_playbook.md).** The read-only investigation that produced the exact mutations the forward-fix executed. Includes pre-conditions, post-conditions, and reversibility per step. Historical evidence rather than forward-looking work.

## Reports (under `reports/` subdirectory)

These are individual agent run reports, kept as audit trail rather than as forward-looking documents. The next agent does not need to read these to act on TACK-228 or the rest of the work; they are evidence of what happened during the session.

The reports cover backup rebuild and validation, the FDB anonymous-volume defect investigation and the first real `fdbbackup` snapshot, the audit-table investigation that proved the table existed (an earlier agent's "table doesn't exist" finding was a wrong-database query), the production read-only sweep that surfaced the FDB and Temporal-DB backup gaps, the forward-fix and state-repair execution logs, the Phase 1 compliance fix report, the Phase 2 wave 1 producer/consumer/monitoring/runbook reports, the audit parity tooling report, the worktree cleanup logs, and the CLAUDE.md drift fix report. Filenames in `reports/` are descriptive enough to find a specific report when needed.

## What was deleted on 2026-05-10

These files used to live in this directory and were removed during the cleanup. Listed here so the next agent does not look for them.

The five **tarballs** totaling about 83 MB (`fdb-snapshot-20260509T051802Z.tar.gz`, `audit-snapshot-20260509T164222Z.tar.gz`, and three forward-fix recovery snapshots) were deleted because the production backup chain at `/root/backups/tack-20260509T232955Z/` now serves the same purpose. The **`manual_audit_backup/`** subdirectory holding the raw WAL binary from the audit drop investigation was removed; the investigation conclusions live in `reports/audit_table_investigation.md`. The **`state_repair_workdir/`** subdirectory with raw production data was removed for the same reason: the conclusions live in `reports/state_repair_execution_report.md`. The plan documents `audit_log_first_refactor_plan.md` (rejected approach, superseded by the two-phase plan), `audit_scale_research.md` (off-target first pass, superseded by `audit_scale_architectures.md`), `audit_horizontal_design_revision_notes.md` (transient notes from the Redpanda-to-Kafka switch already incorporated into the main design doc), `phase1_verification_checklist.md` (Phase 1 has shipped), and `retro_update_report.md` (meta-report whose content is in `retro_log.md`) were removed. The small post-completion status files `execution_snapshots_manifest.txt`, `execution_status.txt`, and `state_repair_status.txt` were removed.

## Status as of 2026-05-10

The forward-fix is complete and production MCP is working. The audit-WAL silent-drop bug is fixed in production (Phase 1 at commit `23ad44a`). The backup script has been rebuilt in shell and is producing valid backups; the Go replacement is in flight as TACK-228 (mechanical lint pass) plus the partial implementation in the `tack-backup-restore-test` worktree. The Phase 2 wave 1 audit dual-write code is committed at `a5aec6d` but not yet deployed. The deploy subcommand work is partially in main checkout, written against the Docker Go SDK, blocked on a `govulncheck` failure that the next agent must resolve without abandoning the SDK. The address-index drift, the OrgID derivation bug, the slug normalization gap, and the workspace-as-hardcoded-concept architectural leak are all filed as tickets under epic TACK-14 and are dependency-ordered there. The QA environment, stale token cleanup, and incident-mode control plane are filed under epic TACK-16.

The next thing the next agent should do is open HANDOFF.md and follow section 13.
