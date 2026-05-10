# Workflow-State Repair Execution Report

Date: 2026-05-09
Run window start (UTC): 2026-05-09T16:14:55Z
Run window end (UTC): 2026-05-09T16:20:27Z
Operator: alex@goodkind.io

## 1. Verdict

No-op. Stopped at Step 0 (verification) before taking any snapshot or applying any mutation.

The state-repair work described by the playbook had already been applied to production some time before this maintenance window opened. Re-running the audit against current production data found zero remaining rows in any of the four policy buckets. Continuing through Steps 1-10 would have produced no writes, so the run was halted to avoid taking pointless snapshots and standing up the backup_agent sidecar. No FDB writes, no SQL writes, no container changes were made.

This matches the failure-protocol clause: if any apply step's result counts diverge from expected, stop and report. Here the divergence shows up in Step 0: every bucket count was 0 instead of 218 / 3 / 44 / 1.

## 2. Bucket counts before and after

The audit baseline at `/Users/agoodkind/Sites/tack/state_audit_full_impact.md` is dated 2026-05-05 and identifies 562 affected rows total. I re-ran an equivalent classification against current production via `./server ops inspect find --org 019dc5ad-0408-7e43-9c4d-d3e6736ac058 --type {issue,epic}` and cross-referenced every audit row by UUID.

| Bucket | Audit baseline 2026-05-05 | Current production 2026-05-09T16:18Z |
|---|---|---|
| `missing_state_id_unresolved` (policy 1+2) | 221 | 0 |
| -- of which raw alias is `open` (policy 1) | 218 | 0 |
| -- of which raw alias is `in_progress` (policy 2) | 2 | 0 |
| -- of which raw alias is `in-progress` (policy 2) | 1 | 0 |
| `no_state` (policy 3) | 44 | 0 |
| `typed_only_invalid` (policy 4) | 1 | 0 |
| `missing_state_id_resolvable` | 147 | 0 |
| `raw_matches_typed` | 78 | 0 |
| `raw_unresolved_typed_valid` | 51 | 0 |
| `raw_mismatches_typed` | 20 | 0 |
| `typed_only_valid` | 105 | 815 (all live issues + epics) |
| Total live issues | 607 | 755 |
| Total live epics | 60 | 60 |

Issue count rose from 607 to 755 between the audit baseline and now; the additional 148 issues are unrelated to this lane and all classify as `typed_only_valid`.

For all 562 audit rows, the current production state is:

```
('missing_state_id_resolvable', 'valid_state_id_only')   147
('missing_state_id_unresolved', 'valid_state_id_only')   221
('no_state',                    'valid_state_id_only')    44
('raw_matches_typed',           'valid_state_id_only')    78
('raw_mismatches_typed',        'valid_state_id_only')    20
('raw_unresolved_typed_valid',  'valid_state_id_only')    48
('raw_unresolved_typed_valid',  'MISSING')                 3
('typed_only_invalid',          'valid_state_id_only')     1
```

`MISSING` means the audit row is not present in the current production snapshot. The three rows are CLYDE-79, CLYDE-80, and CLYDE-86; all were classified as `raw_unresolved_typed_valid` in the baseline (typed `state_id` was already valid). They appear to have been deleted between 2026-05-05 and now. No policy bucket is affected.

## 3. Mutations applied per policy bucket

None during this run.

I confirmed no application code or repair tooling change was needed. The current `./server ops repair classes` output lists three classes (`reference_property`, `parent_reference`, `props_transform`) and does not include `stray_alias_state` referenced in the playbook. The work was performed with the current toolset some time before this window.

The pre-existing state mutations (visible via `updated_at` on the formerly-affected rows) all share `updated_at` timestamps near `2026-05-07T01:30:13...18Z`. The repaired values for the policy buckets were:

- Policy 1 (`open` -> `Todo`): 216 of 218 rows landed on project-scoped `Todo`. 2 rows landed on project-scoped `Done` rather than `Todo`. Both are CLYDE rows on epics from the `open` alias bucket.
- Policy 2 (`in_progress` / `in-progress` -> `In Progress`): 3 of 3 rows landed on project-scoped `In Progress`. Matches policy.
- Policy 3 (no-state -> `Backlog`): 0 of 44 rows landed on `Backlog`. 26 MWAN rows landed on `MWAN::Todo`, 14 TACK rows landed on `TACK::Todo`, 4 MWAN rows landed on `MWAN::Done`. The implemented mapping appears to have been "no-state -> `Todo`" rather than "no-state -> `Backlog`".
- Policy 4 (CLYDE-21 string `Backlog` -> CLYDE Backlog UUID): 1 of 1 row landed on `CLYDE::Backlog` (`019dd22f-7afd-7356-b81f-bf0a1591e39d`). Matches policy.

## 4. Snapshots taken

None. The plan calls for a pre-snapshot at Step 2 and per-stage snapshots after Steps 3-7. With no mutations to apply, none were taken. The backup_agent sidecar described in Step 1 was not started. No tarball was bundled or copied offsite.

The wave-1 forward-fix earlier today did produce three snapshots already (`snapshot-after-phase3`, `snapshot-after-phase6`, `snapshot-final`) which are still present at `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/`. They are a different lane and are not state-repair snapshots.

## 5. MCP validation results

Per Step 8, I sampled one row from each policy bucket via MCP and verified state resolution:

| Sample row | Policy | Expected state | MCP `tack_get_*` returned |
|---|---|---|---|
| APP-1 (`019dc5ed-7bb1-734e-9929-16acce957a91`, epic) | 1 | `APP::Todo` | `APP::Todo`. Pass. |
| CLYDE-1 (`019dc5ed-6c9c-75c4-9707-159cfbdcdbbd`, epic) | 2 | `CLYDE::In Progress` | `CLYDE::In Progress`. Pass. |
| MWAN-2 (`019de68c-84e7-75db-9b82-88be888d2a99`, epic) | 3 | `MWAN::Backlog` per operator policy | `MWAN::Todo` (diverges from operator policy 3, see anomaly 3). |
| CLYDE-21 (`019de1a2-75ba-71a7-a4b2-632816d2c958`, epic) | 4 | `CLYDE::Backlog` | `CLYDE::Backlog`. Pass. |

All four MCP calls succeeded and returned a project-scoped state name without any error or stale `state` raw alias visible.

Production cluster health checks at the same time:

- `docker exec tack-fdb-1 fdbcli --exec "status"` returned `Replication health - Healthy` and `Fault Tolerance - 0 machines`.
- `docker ps`: `tack-app-1 Up 12 hours`, `tack-yugabyte-1 Up 4 days (healthy)`, `tack-meilisearch-1 Up 5 days (healthy)`, `tack-fdb-1 Up 5 days (healthy)`, `tack-temporal-ui-1 Up 4 days`, `tack-temporal-1 Up 5 days (healthy)`, `tack-temporal-db-1 Up 5 days (healthy)`. Uptimes match the wave-1 baseline; no production container was restarted, recreated, or scaled.

## 6. Final state inventory by bucket

Run against all 815 live issues and epics in OLD org `019dc5ad-0408-7e43-9c4d-d3e6736ac058`:

| Bucket | Issues | Epics |
|---|---|---|
| `typed_only_valid` | 755 | 60 |
| `raw_matches_typed` | 0 | 0 |
| `raw_unresolved_typed_valid` | 0 | 0 |
| `raw_mismatches_typed` | 0 | 0 |
| `missing_state_id_resolvable` | 0 | 0 |
| `missing_state_id_unresolved` | 0 | 0 |
| `no_state` | 0 | 0 |
| `typed_only_invalid` | 0 | 0 |

Every live workflow row has a valid project-scoped `state_id` and no raw `state` alias. There is no remaining work in this lane.

## 7. Anomalies and surprises

1. The state-repair work was already applied before this maintenance window. Sample updated_at timestamps on the formerly-affected rows cluster around `2026-05-07T01:30:13...18Z`, two days before this run. I have no audit-ledger record of the operation: `audit.events` returns "relation does not exist" inside YugabyteDB, and the MCP audit tool only exposes `node.create`, `node.update`, `node.read`, etc. against `mcp_tool` actor records, not `repair.apply` events. The repair was therefore performed via `./server ops repair apply` (or equivalent) but its history is not surfaced through any tool I had access to. Suggesting an operator follow-up to capture how that prior run was executed and where its artifacts live.

2. `./server ops repair classes` does not include `stray_alias_state`. The playbook references that class by name and the related Go file `repair_stray_alias_state.go`. Neither exists in the current `internal/ops/` directory. The repair architecture has been refactored into three generic classes: `reference_property`, `parent_reference`, `props_transform`. The earlier wave-1 lane on body/description/parent/status used these. If this work needed to be re-applied, it would now be expressed as one or more `reference_property` profiles with a `value_map` for the `open` / `in_progress` / `in-progress` aliases plus per-row manifests for the `no_state` and `Backlog`-string cases. No code change is needed to do that.

3. Policy 3 (no-state -> `Backlog`) was not implemented as described. The 44 audit rows in `no_state` now have `Todo` (40 rows: 26 MWAN, 14 TACK) or `Done` (4 MWAN rows). 0 of 44 went to `Backlog`. If the operator considers the actual outcome correct, this is a documentation issue; if `Backlog` was the desired outcome, those 44 rows still need a follow-up correction. They are not in any malformed bucket today, so this is a semantic mismatch rather than a structural defect.

4. Policy 1 had two outlier rows. 216 of 218 rows mapped `open` -> `Todo` per policy. 2 CLYDE epics ended up at `Done` instead. Same shape of issue as anomaly 3: structurally clean, may be semantically wrong. Worth a single spot-fix if the operator wants exact policy compliance.

5. Three audit rows are missing from production: CLYDE-79, CLYDE-80, CLYDE-86. All three were classified as `raw_unresolved_typed_valid` (already-valid `state_id` plus a stale raw alias) at audit time, so their disappearance does not affect any policy bucket. Worth confirming with the operator that these were intentional deletions.

6. The 7 production containers were untouched by this run. uptimes (`Up 12 hours` on app, `Up 4-5 days` on the rest) match the wave-1 forward-fix completion state.

## 8. Operator follow-ups

1. Confirm the prior repair was intentional and authorized. The prior run's manifests, profiles, and apply outputs would document exactly which rows changed and to which target state. If they exist, file them under `/Users/agoodkind/Sites/tack/repair_artifacts/` for the next operator. If they do not exist, that is a reproducibility gap worth closing.

2. Decide on the policy 3 mismatch. Either accept the implemented mapping (no-state -> `Todo`/`Done` per project) and update the playbook, or run a targeted correction: for the 44 rows in the audit's `no_state` bucket, set state_id to each project's `Backlog` UUID. The list of 44 row IDs is preserved in `/Users/agoodkind/Sites/tack/state_audit_affected_rows.csv` filtered by `status="no_state"`.

3. Decide on the two policy 1 outliers (CLYDE epics with `open` alias that landed on `Done` instead of `Todo`). Either accept or correct.

4. Restore the audit ledger. `audit.events` not existing in YugabyteDB is unexpected given the project schema documents `audit.events`, `audit.chain_heads`, `audit.notarizations`, `audit.pii`. Either the schema migrations have not been applied to production, or the audit pipeline is wired to a different schema. This is independent of the state-repair lane but worth a focused investigation: a compliance audit ledger that does not exist cannot satisfy the project's stated SQL contract.

5. Update the workflow-state repair playbook for the new repair tooling. The current document references `stray_alias_state` and `repair_stray_alias_state.go`; both have been removed. The replacement classes (`reference_property` with `value_map` normalization, plus `props_transform` for direct-property cleanup) can express the same intent. Including a worked example would future-proof the playbook for the next legacy-alias batch.

## 9. Files produced

- This report: `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/state_repair_execution_report.md`
- One-line status: `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/state_repair_status.txt`
- Verification working data: `/Users/agoodkind/Sites/tack/incident_2026-05-09_seed_parallel_org/state_repair_workdir/projects.json`, `states_by_project.json`, `audit_live.json`

No FDB snapshots, no manifests, no apply outputs were generated by this run because no mutation was attempted.
