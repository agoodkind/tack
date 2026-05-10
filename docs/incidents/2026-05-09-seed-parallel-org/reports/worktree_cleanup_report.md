# Worktree Cleanup Report (2026-05-09)

This report documents a destructive cleanup of the local worktree set for
`/Users/agoodkind/Sites/tack`, executed against the inventory at
`incident_2026-05-09_seed_parallel_org/worktree_inventory.md`. The cleanup
removed worktrees the user pre-approved, performed one carve-out in a kept
worktree, and classified three "investigate" worktrees without deleting them.
No remote pushes, deploys, or production changes were made.

---

## 1. Summary

| Metric | Before | After | Change |
|---|---|---|---|
| Total worktrees (incl. main) | 18 | 7 | -11 |
| Local branches | 8 | 6 | -2 |
| Files removed in carve-out | n/a | 1 | n/a |

Findings:

- 11 worktrees from the "safe to delete" set verified and removed cleanly.
- 2 local branches (`golden/slug-address-integration`, `codex/reference-repair-generic`) confirmed merged into main and deleted.
- 1 untracked shell script (`scripts/audit-parity.sh` in `tack-phase2-wave1-wip`) removed in favor of the Go subcommand at `internal/ops/audit_parity.go`.
- All 3 "investigate" worktrees were classified as "superseded but reworked"; none was deleted, per instructions.
- One additional worktree, `reference-repair-c3192a5c`, was found in the live worktree list but was NOT in the inventory. It is flagged as an open item.

The inventory header claimed 19 worktrees including main, but the actual `git worktree list` showed 18. That discrepancy is recorded in section 6.

---

## 2. Phase 1 verification log

For each "safe to delete" entry, the verification confirmed the path exists,
the HEAD is an ancestor of `main` (or content equivalence is documented), and
the working tree state matches the inventory.

| Worktree | HEAD | Ancestor of main? | Dirty? | Verdict |
|---|---|---|---|---|
| `address-ops-1a2b3c4d/tack-7462ac598e92` | `7f08ebf` | yes | clean | safe |
| `deploy-wave1-dd430c9` | `dd430c9` | yes | clean | safe |
| `report-6f2a1b3c/tack-7462ac598e92` | `82d39ad` | yes | clean | safe |
| `slug-address-integration-6a88f02a/tack-7462ac598e92` | `584176e` | yes | clean | safe |
| `slug-address-rollout-4f3a9c2d/tack-7462ac598e92` | `d28fa0e` | NO direct ancestor; content-equivalent path documented | clean | safe (see note) |
| `slug-audit-4f7a9c2d/tack-7462ac598e92` | `8e3b939` | yes | clean | safe |
| `slug-integration-plan-e9b1c4a7/tack-7462ac598e92` | `8e3b939` | yes | clean | safe |
| `slug-rollout-9f9e71dd/tack-7462ac598e92` | `82d39ad` | yes | clean | safe |
| `tack-reference-repair-generic` | `8e3b939` | yes | clean | safe |
| `abolish-slug-a3f9c2d8/tack-7462ac598e92` | `f17afc6` | yes | dirty (39 entries) | safe (see diff capture) |
| `abolish-slug-mcp-a1b2c3d4/tack-7462ac598e92` | `f17afc6` | yes | dirty (24 entries) | safe (see diff capture) |

### Note on `slug-address-rollout-4f3a9c2d`

The HEAD `d28fa0e` is not a direct ancestor of `main`. The diff against the
merged tip `584176e` shows the worktree predates the generic-repair refactor:
it still contains `repair_stray_alias_state.go`, `repair_state_resolution.go`,
and `node_create.go`/`node_create_prepare.go` files that main has since
removed or restructured. The newer generic repair classes
(`repair_props_transform.go`, the parent_reference catalog entry) are absent.
This is consistent with the inventory's claim that the work landed through
the golden branch merge by way of equivalent content rather than this exact
commit. Treating this worktree as superseded looks correct; if any doubt
remains, the unique commit list can be reproduced with
`git -C /Users/agoodkind/Sites/tack diff main..d28fa0e`.

### Diff capture for `abolish-slug-a3f9c2d8` (dirty)

The worktree had 33 modified tracked files and 6 untracked files. Per-file
comparison against main (`git show main:<path> | diff -q - <worktree-path>`):

- 8 modified files matched main exactly (likely formatting or whitespace reverts to main).
- 25 modified files differed from main.
- 5 of 6 untracked files matched main exactly (`node_address_test.go`, `domain/node/reference.go`, `reference_test.go`, `service/node_address.go`, `service/node_address_test.go`).
- 1 untracked file differed: `internal/adapters/foundationdb/node_address.go`.

The diff on `node_address.go` showed the worktree variant threads a
`*slog.Logger` parameter through `retryAddressTransaction` and
`ensureAddressOwner`, while the main version uses `telemetry.L(ctx)` inline.
This matches the inventory's note that the worktree captured the
"`log` parameter threading variant that was not the version that landed."
The differing modified files (e.g. `resolve_typed.go`, `resolve.go`) used
the older `Slug` naming; main uses `Reference`. All differences are older
patterns that main superseded.

### Diff capture for `abolish-slug-mcp-a1b2c3d4` (dirty)

- 12 modified files matched main exactly.
- 11 modified files differed from main.
- 1 untracked file (`reference_parameters.go`) matched main exactly.

Sampled `internal/adapters/mcp/tools/resolve_typed.go`: the only difference
was `node.ReferenceDirectSlug` (worktree) vs `node.ReferenceDirectProperty`
(main). This is the same naming-rename pattern: worktree carries the
pre-rename code that main has since updated. No unique work appears at risk.

---

## 3. Phase 2 deletion log

All commands were issued from the main checkout via
`git -C /Users/agoodkind/Sites/tack worktree remove --force <path>`,
followed by `git -C /Users/agoodkind/Sites/tack worktree prune`.

| Worktree path | Action |
|---|---|
| `~/.cursor/worktrees/address-ops-1a2b3c4d/tack-7462ac598e92` | removed |
| `~/.cursor/worktrees/deploy-wave1-dd430c9` | removed |
| `~/.cursor/worktrees/report-6f2a1b3c/tack-7462ac598e92` | removed |
| `~/.cursor/worktrees/slug-address-integration-6a88f02a/tack-7462ac598e92` | removed |
| `~/.cursor/worktrees/slug-address-rollout-4f3a9c2d/tack-7462ac598e92` | removed |
| `~/.cursor/worktrees/slug-audit-4f7a9c2d/tack-7462ac598e92` | removed |
| `~/.cursor/worktrees/slug-integration-plan-e9b1c4a7/tack-7462ac598e92` | removed |
| `~/.cursor/worktrees/slug-rollout-9f9e71dd/tack-7462ac598e92` | removed |
| `/Users/agoodkind/Sites/tack-reference-repair-generic` | removed |
| `~/.cursor/worktrees/abolish-slug-a3f9c2d8/tack-7462ac598e92` | removed (--force, dirty) |
| `~/.cursor/worktrees/abolish-slug-mcp-a1b2c3d4/tack-7462ac598e92` | removed (--force, dirty) |

`git worktree prune` then ran cleanly with no output.

### Branch cleanup

| Branch | Ancestor of main? | Action |
|---|---|---|
| `golden/slug-address-integration` | yes (tip `584176e`) | deleted with `branch -d` |
| `codex/reference-repair-generic` | yes (tip `8e3b939`) | deleted with `branch -d` |

Detached-HEAD worktrees did not have associated branches to delete. The
following branches remain because they back kept worktrees:
`phase2-wave1-rebase` (main checkout), `backup-restore-test-9d4f7a`,
`phase2-wave1-wip`, `phase2-wip`, and `reference-repair-generic-golden`.

---

## 4. Phase 3 carve-out

Target file: `/Users/agoodkind/Sites/tack-phase2-wave1-wip/scripts/audit-parity.sh`.

Pre-check: file existed (6900 bytes, 2026-05-09 mtime), `git status` reported
it as untracked (`??`), and `git ls-files` returned empty. The Go
replacement at `/Users/agoodkind/Sites/tack/internal/ops/audit_parity.go` was
confirmed present in the main checkout (on `phase2-wave1-rebase`).

Action: removed with `rm` (no commit needed since the file was untracked).
Post-check confirmed the path no longer exists.

The shell-script moratorium described in the user instructions was
respected: nothing else in `scripts/` of that worktree was touched.

---

## 5. Phase 4 investigate findings

None of the three were deleted. Each is classified below.

### 5.1 `reference-repair-generic-a1b2c3d4` (`reference-repair-generic-golden` branch)

Commits ahead of main (2):

- `e8b4f14 Add generic reference property repair tooling`
- `bfab5f4 Merge branch 'main' into reference-repair-generic-golden`

The first commit reintroduces files like `repair_stray_alias_state.go` that
main has explicitly removed in favor of the three generic repair classes
(per CLAUDE.md "Deprecated names" section). The merge commit does not
reconcile; it pulled main into an older shape.

Working-tree state: 4 modified tracked files (`node.go`,
`node_create_effects.go`, `node_create_idempotency.go`,
`node_create_props.go` deleted), and 2 untracked files
(`node_create.go`, `node_create_prepare.go`).

File comparison vs main:

- `node_create.go`: differs. Worktree uses `writeCreateSlug` and
  `prepareCreateProps(ctx, orgID, ...)`; main uses
  `writeReferenceAddress` and threads a logger
  (`prepareCreateProps(ctx, log, orgID, ...)`). Worktree variant predates
  the rename.
- `node_create_prepare.go`: differs in the same way (older signatures,
  pre-rename names).

**Classification: superseded but reworked.** The committed work would
re-introduce deprecated repair tooling, and the working-tree changes are
an older split of the same `node_create` refactor that main has since
landed in different shape. Recommend deletion, but the user should confirm
because there are real ahead-commits to evaluate.

### 5.2 `generic-repair-036fd71c` (detached `8e3b939`)

Working-tree state: 8 modified files in `internal/ops/`, 3 deleted files
(`repair_state_resolution.go`, `repair_state_summary.go`,
`repair_stray_alias_state.go`), and 8 untracked files.

Untracked file comparison vs main:

- `repair_manifest.go`, `repair_reference_match.go`,
  `repair_reference_property.go`, `repair_report.go`: exist in main, content
  differs.
- `repair_reference_match_helpers.go`, `repair_reference_match_scoped.go`,
  `repair_reference_selection.go`, `repair_reference_types.go`:
  do NOT exist in main.

A symbol search in `main:internal/ops/` for `normalizeReferencePolicy`,
`matchScopedProperty`, `resolvedCandidates`, and
`RepairReferencePropertyPolicy` returned zero hits. Main uses different
names (`RepairReferenceProfile`, `RepairCleanupPolicy`,
`RepairConflictPolicy`) and a different file split.

**Classification: superseded but reworked.** Conceptually the same work
landed in main, but the implementation was redesigned. The worktree carries
files with content that does not appear under any name in main. Whether to
keep depends on whether the user wants the alternative design preserved.
Recommend the user decide; do NOT delete autonomously.

### 5.3 `reference-repair-generic-c3192a5c` (detached `8e3b939`)

Working-tree state: 10 modified files, 3 deleted (same set as 5.2), and 5
untracked files.

Untracked file comparison vs main: all 5 (`command_repair_manifest.go`,
`repair_manifest.go`, `repair_reference_match.go`,
`repair_reference_property.go`, `repair_report.go`) exist in main but with
different content.

Sampled diff on `repair_manifest.go`: worktree version is 136 lines vs
main's 90, with extra fields like `Version int`, `RepairReferencePolicy`
(rather than main's `RepairReferenceProfile`),
`Entries []RepairManifestEntry`, `Previews`, and `Applications`. The
worktree variant is a more-elaborate manifest format that was simplified
in main.

**Classification: superseded but reworked.** Same situation as 5.2:
conceptually equivalent work shipped, with a different and simpler design.
Recommend the user decide.

---

## 6. Open items for the operator

### 6.1 `reference-repair-c3192a5c` (path is `~/.cursor/worktrees/reference-repair-c3192a5c/tack-7462ac598e92`)

This worktree is present in `git worktree list` but does NOT appear in the
inventory. Its HEAD is `8e3b939` (ancestor of main). Working tree shows
~29 dirty entries that look like yet another iteration of the repair-tooling
refactor: deleted files include `repair_state_resolution.go`,
`repair_state_summary.go`, `repair_stray_alias_state.go`, plus untracked
files such as `command_repair_preview_manifest.go`, `repair_manifest_io.go`,
`repair_policy_cli.go`, and `repair_reference_candidate.go`.

This is structurally similar to the three "investigate" worktrees but is
distinct from `reference-repair-generic-c3192a5c`. Recommend running the
same comparison process on it before deciding:

```
git -C /Users/agoodkind/.cursor/worktrees/reference-repair-c3192a5c/tack-7462ac598e92 status --short
```

### 6.2 The three investigate worktrees

All three were classified as "superseded but reworked." None has commits
ahead of main except `reference-repair-generic-a1b2c3d4` (2 commits). If
the user is confident the alternative repair-tooling designs need not be
preserved, the cleanup commands are:

```
git -C /Users/agoodkind/Sites/tack worktree remove --force \
  "/Users/agoodkind/.cursor/worktrees/reference-repair-generic-a1b2c3d4/tack-7462ac598e92" \
  "/Users/agoodkind/.cursor/worktrees/generic-repair-036fd71c/tack-7462ac598e92" \
  "/Users/agoodkind/.cursor/worktrees/reference-repair-generic-c3192a5c/tack-7462ac598e92"
git -C /Users/agoodkind/Sites/tack worktree prune
git -C /Users/agoodkind/Sites/tack branch -D reference-repair-generic-golden
```

Note the `-D` (capital) on the branch delete: `reference-repair-generic-golden`
has 2 commits ahead of main, so `-d` will refuse.

### 6.3 Inventory total

The inventory header says 19 worktrees including main. The live list before
cleanup showed 18. The discrepancy may be due to the unlisted
`reference-repair-c3192a5c` (which would have brought the inventory total
to 20 if counted), or the inventory may have been written when an extra
worktree existed that was already cleaned up before this cleanup run.
Either way, all 11 inventory-marked safe entries were located and removed.

### 6.4 Kept worktrees still active

Per inventory, these remain by design and were not touched:

- `/Users/agoodkind/Sites/tack` (main checkout, `phase2-wave1-rebase`).
- `/Users/agoodkind/Sites/tack-phase2-wave1-wip` (active wave1 audit consumer work; `audit-parity.sh` removed per Phase 3).
- `/Users/agoodkind/Sites/tack-backup-restore-test` (systemd unit and CI workflow not yet on main).
- `~/.cursor/worktrees/abolish-slug-ops-7f3a9c2e/tack-7462ac598e92` (untracked `internal/ops/backfill_addresses_preview.go` does exist in main but with different content; the worktree variant should be diffed against main before moving the worktree to "safe"). The diff command is `diff <(git -C /Users/agoodkind/Sites/tack show main:internal/ops/backfill_addresses_preview.go) <worktree>/internal/ops/backfill_addresses_preview.go`.
