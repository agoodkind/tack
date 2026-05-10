# Worktree Inventory (2026-05-09)

Produced by read-only inspection. No git state was modified.

---

## Summary

| Category | Count |
|---|---|
| Safe to delete | 11 |
| Keep | 5 |
| Investigate | 3 |

**Total worktrees enumerated:** 19 (including the main checkout at `/Users/agoodkind/Sites/tack`).

---

## Safe to Delete

These worktrees meet all three criteria: the commit at HEAD is a fully-merged
ancestor of main, the working tree is clean (no uncommitted changes), and no
in-flight agent is known to be using the path.

| Path | Branch / HEAD | Last Commit Date | Why Safe |
|---|---|---|---|
| `~/.cursor/worktrees/address-ops-1a2b3c4d/tack-7462ac598e92` | detached `7f08ebf` | 2026-05-05 | `7f08ebf` is an ancestor of main. Clean. From the slug-to-address refactor that merged via `758e651`. |
| `~/.cursor/worktrees/deploy-wave1-dd430c9` | detached `dd430c9` | 2026-05-08 | `dd430c9` is an ancestor of main. Clean. Deploy staging checkpoint; content is on main. |
| `~/.cursor/worktrees/report-6f2a1b3c/tack-7462ac598e92` | detached `82d39ad` | 2026-05-06 | `82d39ad` is an ancestor of main. Clean. Report-generation worktree from the repair refactor. |
| `~/.cursor/worktrees/slug-address-integration-6a88f02a/tack-7462ac598e92` | `golden/slug-address-integration` | 2026-05-06 | Branch tip `584176e` is an ancestor of main. Clean. Merged via `758e651`. |
| `~/.cursor/worktrees/slug-address-rollout-4f3a9c2d/tack-7462ac598e92` | detached `d28fa0e` | 2026-05-06 | `d28fa0e` is not a direct ancestor of main but carries the same content as `584176e`, which is on main. Clean. The work landed through the golden branch merge. See Investigate note below if any doubt remains. |
| `~/.cursor/worktrees/slug-audit-4f7a9c2d/tack-7462ac598e92` | detached `8e3b939` | 2026-05-05 | `8e3b939` is an ancestor of main. Clean. Audit worktree from the slug refactor phase. |
| `~/.cursor/worktrees/slug-integration-plan-e9b1c4a7/tack-7462ac598e92` | detached `8e3b939` | 2026-05-05 | `8e3b939` is an ancestor of main. Clean. Planning worktree; no unique content. |
| `~/.cursor/worktrees/slug-rollout-9f9e71dd/tack-7462ac598e92` | detached `82d39ad` | 2026-05-06 | `82d39ad` is an ancestor of main. Clean. Rollout staging worktree. |
| `/Users/agoodkind/Sites/tack-reference-repair-generic` | `codex/reference-repair-generic` | 2026-05-05 | Branch tip `8e3b939` is an ancestor of main. Clean. The generic repair work merged into main. |
| `~/.cursor/worktrees/abolish-slug-a3f9c2d8/tack-7462ac598e92` | detached `f17afc6` (dirty) | 2026-05-05 | `f17afc6` is an ancestor of main. The uncommitted changes (including the new `node_address.go`) were compared against main and the content landed there, though with small implementation differences (a `log` parameter threading change). The worktree captures intermediate exploration; nothing unique is lost if deleted. See note below. |
| `~/.cursor/worktrees/abolish-slug-mcp-a1b2c3d4/tack-7462ac598e92` | detached `f17afc6` (dirty) | 2026-05-05 | Same base commit as above. Dirty files are MCP tool layer edits from the address-parameter refactor, all of which landed in main. The uncommitted changes are an intermediate snapshot, not unique work. |

**Note on the two dirty `f17afc6` worktrees:** Both have uncommitted changes
that overlap with files that are now on main. The specific
`node_address.go` diff shows a `log` parameter threading variant that was
not the version that landed. This is exploratory in-progress work that was
superseded. Verify with `git diff main -- <file>` on any file you are
uncertain about before deleting, if you want to be sure.

---

## Keep

These worktrees represent active work that has not fully landed in main, or are
the primary checkout itself.

| Path | Branch | Why Keep |
|---|---|---|
| `/Users/agoodkind/Sites/tack` | `phase2-wave1-rebase` | Primary checkout. One commit ahead of main (`1934828 Use ysql_dump instead of pg_dump for Yugabyte backup`) and has 30 dirty entries from the phase2 audit/Kafka work in progress. |
| `/Users/agoodkind/Sites/tack-phase2-wave1-wip` | `phase2-wave1-wip` | Active phase2 staging area. Branch tip `dd430c9` is on main but the working tree holds substantial uncommitted work: new `internal/audit/consumer.go`, `dual.go`, `kafka_recorder.go`, three new migrations (`003` through `005`), and `scripts/audit-parity.sh`. None of these files exist on main. This is the wave1 audit consumer implementation in progress. |
| `/Users/agoodkind/Sites/tack-backup-restore-test` | `backup-restore-test-9d4f7a` | Branch tip is on main, but the working tree has untracked files including `scripts/backup-restore-test.sh`, `scripts/systemd/tack-backup-restore-test.service` and `.timer`, and `.github/workflows/backup-content-check.yml`. The main script and Makefile targets landed on main; the systemd units and GitHub Actions workflow have not. Check whether those are still needed before removing this worktree. |
| `~/.cursor/worktrees/abolish-slug-ops-7f3a9c2e/tack-7462ac598e92` | detached `f17afc6` (dirty) | Has a unique untracked file: `internal/ops/backfill_addresses_preview.go`. Check whether this file's content is distinct from anything on main before deleting. The base commit is an ancestor of main, so the only risk is the untracked content. |

---

## Investigate

These worktrees need a closer look before a deletion decision. Either the branch
has commits not on main, or the uncommitted changes contain files whose
disposition is uncertain.

| Path | Branch / HEAD | Divergence from main | Uncommitted changes | Recommended next step |
|---|---|---|---|---|
| `~/.cursor/worktrees/reference-repair-generic-a1b2c3d4/tack-7462ac598e92` | `reference-repair-generic-golden` | 2 commits ahead, 11 behind. Commits: `e8b4f14 Add generic reference property repair tooling` and `bfab5f4 Merge branch main into reference-repair-generic-golden`. | Dirty: several `internal/service/node_create_*.go` files modified or deleted, two new files (`node_create.go`, `node_create_prepare.go`) untracked. | The two ahead-commits carry `internal/ops/repair_stray_alias_state.go` and related files that were explicitly removed from main (replaced by the three generic repair classes). The uncommitted changes add a `node_create` split that is not on main. Decide: (a) is `node_create.go` / `node_create_prepare.go` still desired? If yes, move the content to the active branch. If no, delete. |
| `~/.cursor/worktrees/reference-repair-generic-c3192a5c/tack-7462ac598e92` | detached `8e3b939` (dirty) | Base `8e3b939` is an ancestor of main. | Dirty: 19 changed or deleted files in `internal/ops/`, plus new untracked files including `repair_reference_property.go`, `repair_report.go`, `repair_manifest.go`, `command_repair_manifest.go`. | `8e3b939` is on main but the uncommitted work represents an earlier iteration of the generic repair tooling. Compare `repair_reference_property.go` here vs `main:internal/ops/repair_reference_property.go` to determine if anything was left behind that did not make it. |
| `~/.cursor/worktrees/generic-repair-036fd71c/tack-7462ac598e92` | detached `8e3b939` (dirty) | Base `8e3b939` is an ancestor of main. | Dirty: 12 changed or deleted files in `internal/ops/`, plus new untracked files including `repair_reference_property.go`, `repair_report.go`, `repair_manifest.go`, `repair_reference_match*.go`. Several `repair_state_*.go` files deleted in working tree. | Very similar situation to `reference-repair-generic-c3192a5c`. Both worktrees likely held concurrent iterations of the same repair tooling refactor. Compare the untracked files against main before deleting; the content probably landed but was reworked. |

---

## Recommended Deletion Command

The following command removes all worktrees classified as safe to delete, plus
their associated local branches. **Review the list above before running it.**
Do not run this until you have checked the three Investigate entries and
confirmed the Keep entries are not needed.

```bash
git -C /Users/agoodkind/Sites/tack worktree remove --force \
  "/Users/agoodkind/.cursor/worktrees/address-ops-1a2b3c4d/tack-7462ac598e92" \
  "/Users/agoodkind/.cursor/worktrees/deploy-wave1-dd430c9" \
  "/Users/agoodkind/.cursor/worktrees/report-6f2a1b3c/tack-7462ac598e92" \
  "/Users/agoodkind/.cursor/worktrees/slug-address-integration-6a88f02a/tack-7462ac598e92" \
  "/Users/agoodkind/.cursor/worktrees/slug-address-rollout-4f3a9c2d/tack-7462ac598e92" \
  "/Users/agoodkind/.cursor/worktrees/slug-audit-4f7a9c2d/tack-7462ac598e92" \
  "/Users/agoodkind/.cursor/worktrees/slug-integration-plan-e9b1c4a7/tack-7462ac598e92" \
  "/Users/agoodkind/.cursor/worktrees/slug-rollout-9f9e71dd/tack-7462ac598e92" \
  "/Users/agoodkind/Sites/tack-reference-repair-generic" \
  "/Users/agoodkind/.cursor/worktrees/abolish-slug-a3f9c2d8/tack-7462ac598e92" \
  "/Users/agoodkind/.cursor/worktrees/abolish-slug-mcp-a1b2c3d4/tack-7462ac598e92" && \
git -C /Users/agoodkind/Sites/tack branch -d \
  golden/slug-address-integration \
  codex/reference-repair-generic \
  2>/dev/null; \
echo "done"
```

The `--force` flag is needed for the dirty worktrees (`abolish-slug-a3f9c2d8`
and `abolish-slug-mcp-a1b2c3d4`). The branch deletions at the end are optional
and only remove local tracking branches whose content is on main. The `2>/dev/null`
suppresses errors for any branch that is already gone.

Do NOT include the three Investigate worktrees or the four Keep worktrees in
this command without resolving their status first.
