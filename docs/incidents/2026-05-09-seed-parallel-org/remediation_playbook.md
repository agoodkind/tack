# Remediation Playbook: Seed-Created Parallel Org / Workspace (2026-05-09)

## 1. Executive summary

- **Scope of mutation**: 3 NodeType JSON rewrites under OLD org, 2 `address_index` repoints, ~37 FDB key clears for NEW org keyspace + 2 global resolve clears, 1 SQL row delete, 1 `backfill.addresses.apply` invocation. No data under OLD org is rewritten or deleted.
- **Total mutations** (point estimate): ~45 FDB key writes/clears + 1 SQL `DELETE`. All mutations are idempotent or trivially reversible from the inspection record below.
- **Estimated operator time**: ~30 to 45 minutes, including read-only verification at every checkpoint.
- **Reversibility**: Phase 1 (NodeType strategy rewrite) is reversible by writing the prior JSON back. Phase 2/3 (delete NEW org keyspace) is logically irreversible at the FDB level, but trivially reproducible by re-running `./server seed`. Phase 4 (`address_index` repoint) is reversible by repointing back. Phase 5 (`org_members` SQL row) is reversible by re-INSERT. Phase 6 (`backfill.addresses.apply`) is reversible by clearing the rows it wrote.
- **Order matters**: NEW org cleanup MUST happen before `backfill.addresses.apply`, otherwise apply will refuse to run. The existing `address_index` rows for `goodkind-io` and `main` resolve to NEW UUIDs, which triggers `addressBackfillStatusConflict` per `backfill_addresses_plan.go:117-125`.

## 2. State inventory before remediation

### NEW (empty) org `3dc1c593-35ea-5214-a198-800e9f38916a`

| Family | Count | Key shape |
|---|---|---|
| `node_instance` | 2 | `(node_instance, NEW_ORG, "org" or "workspace", nodeID)` |
| `node_view` | 2 | `(node_view, NEW_ORG, "org" or "workspace", nodeID)` |
| `node_resolve` (GLOBAL) | 2 | `(node_resolve, NEW_ORG)` and `(node_resolve, NEW_WS)` |
| `node_by_property` | 2 | one row for `org slug "goodkind-io"`, one for `workspace slug "main"` |
| `relationship` | 1 | `(relationship, NEW_ORG, NEW_WS, "child_of", NEW_ORG)` |
| `relationship_reverse` | 1 | `(relationship_reverse, NEW_ORG, NEW_ORG, "child_of", NEW_WS)` |
| `node_type_def` | 11 | `(node_type_def, NEW_ORG, typeID)` |
| `property_def` | 14 | `(property_def, NEW_ORG, defID)` |
| `idempotency_key` | 0 | none |
| `sequence` | 0 | none |

NEW workspace `351ebbfa-3e8b-5ed5-9ae9-65a2eac2ce35` has its `node_instance` and `node_view` rows under the NEW_ORG prefix, so the org-prefix clearrange covers them. The two `node_resolve` rows are global and need explicit clears (one for NEW_ORG, one for NEW_WS).

### OLD (real) org `019dc5ad-0408-7e43-9c4d-d3e6736ac058`

- `node_instance` keys: 1091
- `node_type_def` records with `direct_slug` (must be rewritten): 3

| TypeID | type_key | reference.property | reference.strategy (now) | target strategy |
|---|---|---|---|---|
| `50d61808-a693-51ad-8b0f-dd2ad75141fa` | org | slug | direct_slug | direct_property |
| `bdf72aee-7fff-5449-a69c-108f4682233f` | workspace | slug | direct_slug | direct_property |
| `e69bf784-543b-5937-95d3-3d7772b44950` | project | identifier | direct_slug | direct_property |

The other 8 OLD-org NodeType records use `scoped_sequence`, `scoped_property`, or `uuid_only`. The deployed code accepts those strategies, so they are out of scope.

### Generic address index (global, single-key)

```
(address_index, "org", "primary", "goodkind-io")  -> 3dc1c593-35ea-5214-a198-800e9f38916a   [NEW org]
(address_index, "workspace", "primary", "main")  -> 351ebbfa-3e8b-5ed5-9ae9-65a2eac2ce35   [NEW workspace]
```

These are the only two `address_index` rows currently in production.

### Legacy `slug_index` (also global)

```
(slug_index, "org", "goodkind-io")     -> 019dc5ad-0408-7e43-9c4d-d3e6736ac058   [OLD org]
(slug_index, "workspace", "main")      -> 019dc5ad-0469-71e0-9e73-711bbcc0e93d   [OLD workspace]
(slug_index, "project", "app")         -> 019dc5ed-6af0-793f-add4-1d8964129c8f
(slug_index, "project", "clyde")       -> 019dc5ed-6bf8-7562-a732-9b83a8f8909f
(slug_index, "project", "lab")         -> 019dc5ed-69ae-7404-98da-1822284644 3c
(slug_index, "project", "mwan")        -> 019dc5ed-6925-7aa3-84b3-f430587aac1b
(slug_index, "project", "oss")         -> 019dc5ed-6a3b-7647-9b12-1d7c5e97910c
(slug_index, "project", "tack")        -> 019dc5ed-6825-79fb-a0c5-81 40813b00fb
(slug_index, "project", "website")     -> 019dc5ed-6b73-7db4-83b3-a37200 53a4e3
```

The legacy index is the source of truth for `backfill.addresses.apply`; do not touch it during this remediation.

### SQL state

`org_members` rows for user `14385627-0313-50f8-bf7e-0c966e355dd9` (alex@goodkind.io):

```
(3dc1c593-35ea-5214-a198-800e9f38916a, 14385627-..., 20)   stale, NEW org
(019dc5ad-0408-7e43-9c4d-d3e6736ac058, 14385627-..., 20)   keep, OLD org
```

`audit.events` rows for NEW org/workspace IDs: 0. Seed correctly suppressed audit emission via `audit.WithSuppressed(ctx)` (`cmd/server/seed.go:58`).

### Code references for the keys above

- `internal/adapters/foundationdb/keys.go:17-65` defines all key families.
- `internal/adapters/foundationdb/keys.go:140-142` shows `address_index` is keyed `(nodeType, addressKind, address)`. It is NOT org-scoped, and it stores `nodeID` as raw 16 bytes.
- `internal/adapters/foundationdb/keys.go:120-122` shows `node_resolve` is keyed only by `nodeID`. It is NOT under any org prefix.
- `internal/adapters/foundationdb/inspect_types.go:11` defines `legacySlugIndexKeyFamily = "slug_index"`. The inspect query at `inspect_query.go:152-176` reads `(slug_index, nodeType, addressValue)`.
- The CLAUDE.md `node_address_by_node` reverse index does not exist in code (no references in `internal/`). Only the forward `address_index` row is present, so there is no reverse-index cleanup to do.

## 3. Remediation steps

All commands assume an SSH alias `tack` and run from the host's `/root/tack`. Read-only inspection commands appear under each phase. Mutating commands are clearly labeled `MUTATION`.

### Phase 0: pre-flight snapshot (read-only)

Pre-conditions: server running, FDB and Yugabyte healthy.

```bash
ssh tack 'cd /root/tack && docker compose ps'
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "status"' | head
```

Snapshot the existing data to a temp directory so we can reverse Phase 1/4 if needed.

```bash
ssh tack 'mkdir -p /root/incident_2026_05_09 && cd /root/tack && docker compose exec -T fdb fdbcli --exec "getrange \x02node_type_def\x00\x02019dc5ad-0408-7e43-9c4d-d3e6736ac058\x00 \x02node_type_def\x00\x02019dc5ad-0408-7e43-9c4d-d3e6736ac058\x01 1000"' > /tmp/old_node_type_defs.txt
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "getrange \x02address_index\x00 \x02address_index\x01 1000"' > /tmp/address_index_pre.txt
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "getrange \x02slug_index\x00 \x02slug_index\x01 1000"' > /tmp/slug_index_pre.txt
```

Post-condition: 3 text files exist with current values. These are the rollback inputs for Phase 1 and Phase 4.

Reversibility: N/A (no mutation).

### Phase 1: rewrite OLD org NodeType `direct_slug` to `direct_property`

Pre-conditions:
- The exact target value JSON for each of the 3 NodeType records is known from the snapshot in Phase 0. The change is one substring: `"strategy":"direct_slug"` becomes `"strategy":"direct_property"`. Nothing else in the JSON should change.
- The current strategy enum values that the deployed code accepts are listed at `internal/adapters/mcp/tools/resolve_typed.go:42-53`: `direct_property`, `scoped_sequence`, `scoped_property`, `uuid_only`. `direct_slug` is the unknown strategy this fixes.

Affected FDB keys (3 total, exact tuple form):

```
(node_type_def, "019dc5ad-0408-7e43-9c4d-d3e6736ac058", "50d61808-a693-51ad-8b0f-dd2ad75141fa")  [Org]
(node_type_def, "019dc5ad-0408-7e43-9c4d-d3e6736ac058", "bdf72aee-7fff-5449-a69c-108f4682233f")  [Workspace]
(node_type_def, "019dc5ad-0408-7e43-9c4d-d3e6736ac058", "e69bf784-543b-5937-95d3-3d7772b44950")  [Project]
```

Target values (only the strategy field changes; entire JSON shown for the Org type as the example):

```
{"id":"50d61808-...","org_id":"019dc5ad-0408-...","name":"Org","slug":"org","color":"","icon":"","allowed_ops":["create","read","list","update","delete"],"property_def_ids":null,"plural_slug":"orgs","is_builtin":true,"type_key":"org","features":["has_slug","is_container","is_scope","exclude_from_generic_tools"],"can_contain":["workspace"],"reference":{"strategy":"direct_property","property":"slug"}}
```

Recommended approach: write a small Go utility that uses `NodeStore.GetType` plus `WriteType` (or a single FDB transaction reading-then-writing the same key with the JSON field replaced). Hand-typed `fdbcli set` commands risk JSON corruption.

If a utility is not available, the operator can use `fdbcli`'s `set` after building the new JSON locally. This path is fragile and not recommended.

Operator command (preferred path). The sketch below is pseudocode for a small Go utility:

```
go run ./cmd/devtools/strategy_rewrite \
  --org=019dc5ad-0408-7e43-9c4d-d3e6736ac058 \
  --type-id=50d61808-a693-51ad-8b0f-dd2ad75141fa \
  --type-id=bdf72aee-7fff-5449-a69c-108f4682233f \
  --type-id=e69bf784-543b-5937-95d3-3d7772b44950 \
  --from=direct_slug --to=direct_property
```

If we go the manual `fdbcli` route, build the new JSON locally, then for each key:

```
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "set \x02node_type_def\x00\x02019dc5ad-0408-7e43-9c4d-d3e6736ac058\x00\x0250d61808-a693-51ad-8b0f-dd2ad75141fa\x00 <hex-encoded-new-json>"'
```

Post-conditions:
- Each of the 3 NodeType values now has `"strategy":"direct_property"`.
- Read back via `getrange` and confirm.
- An MCP-side smoke test that does not touch a NEW-org-only feature returns successfully. `tack_describe_workspace` against OLD workspace is a good probe.

Reversibility: yes. The Phase 0 snapshot has the original `direct_slug` JSON for each of the 3 keys.

### Phase 2: clear NEW org's keyspace under each org-scoped key family

Pre-conditions:
- Phase 1 complete and verified.
- No live traffic is creating data under NEW org. The org is empty, so nothing should be pointing at it now.
- Confirm again with: `ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "getrangekeys \x02node_instance\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x00 \x02node_instance\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x01 5"'` returns exactly 2 keys (org plus workspace), nothing else.

Affected key prefixes (all start with `(family, NEW_ORG)`):

```
(node_instance,         "3dc1c593-35ea-5214-a198-800e9f38916a")
(node_view,             "3dc1c593-35ea-5214-a198-800e9f38916a")
(node_by_property,      "3dc1c593-35ea-5214-a198-800e9f38916a")
(relationship,          "3dc1c593-35ea-5214-a198-800e9f38916a")
(relationship_reverse,  "3dc1c593-35ea-5214-a198-800e9f38916a")
(node_type_def,         "3dc1c593-35ea-5214-a198-800e9f38916a")
(property_def,          "3dc1c593-35ea-5214-a198-800e9f38916a")
```

`sequence` and `idempotency_key` are also org-prefixed but currently empty under NEW org. The clearrange below covers them too as a defensive measure even when zero rows exist.

Operator commands. Each is a `clearrange` over `[prefix, prefix \x01)` exclusive upper bound:

```
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "writemode on; clearrange \x02node_instance\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x00 \x02node_instance\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x01"'
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "writemode on; clearrange \x02node_view\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x00 \x02node_view\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x01"'
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "writemode on; clearrange \x02node_by_property\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x00 \x02node_by_property\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x01"'
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "writemode on; clearrange \x02relationship\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x00 \x02relationship\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x01"'
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "writemode on; clearrange \x02relationship_reverse\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x00 \x02relationship_reverse\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x01"'
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "writemode on; clearrange \x02node_type_def\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x00 \x02node_type_def\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x01"'
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "writemode on; clearrange \x02property_def\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x00 \x02property_def\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x01"'
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "writemode on; clearrange \x02sequence\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x00 \x02sequence\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x01"'
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "writemode on; clearrange \x02idempotency_key\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x00 \x02idempotency_key\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x01"'
```

Post-conditions: each `getrangekeys` over each prefix returns 0 rows. Workspace `node_instance` and `node_view` rows are gone because they lived under the NEW_ORG prefix (verified at inspection time).

Reversibility: no. Re-running `./server seed` recreates equivalent records. This is acceptable because the records are deterministic from the seed's deterministic UUIDs and from `service.Seeder.SeedOrg`.

### Phase 3: clear global `node_resolve` rows for NEW org and NEW workspace

Pre-conditions:
- Phase 2 complete (everything else under NEW_ORG keyspace gone).
- Verify resolve rows still exist: `ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "get \x02node_resolve\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x00"'` returns the JSON value, and `get \x02node_resolve\x00\x02351ebbfa-3e8b-5ed5-9ae9-65a2eac2ce35\x00` likewise.

Affected keys (2):

```
(node_resolve, "3dc1c593-35ea-5214-a198-800e9f38916a")    [NEW org]
(node_resolve, "351ebbfa-3e8b-5ed5-9ae9-65a2eac2ce35")    [NEW workspace]
```

Operator commands:

```
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "writemode on; clear \x02node_resolve\x00\x023dc1c593-35ea-5214-a198-800e9f38916a\x00"'
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "writemode on; clear \x02node_resolve\x00\x02351ebbfa-3e8b-5ed5-9ae9-65a2eac2ce35\x00"'
```

Post-conditions: both `get` calls return empty.

Reversibility: no, but trivially recreatable by re-seeding.

### Phase 4: repoint generic `address_index` for `goodkind-io` and `main`

Preferred path: clear, then let backfill rewrite.

Pre-conditions:
- Phase 2 and 3 complete.
- Verify index rows still resolve to the now-deleted NEW UUIDs:
  ```
  ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "getrange \x02address_index\x00 \x02address_index\x01 100"'
  ```
  This should still show the 2 entries pointing at NEW UUIDs.

Two equivalent paths follow.

**4A (preferred)**: clear the two `address_index` rows. Phase 6 (`backfill.addresses.apply`) then rewrites them. This avoids touching the index by hand and uses the existing one-touch tool. The legacy `slug_index` rows (which point at OLD entities) are the source of truth that `backfill.addresses.apply` reads from per `internal/ops/backfill_addresses_preview.go:14-23`.

```
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "writemode on; clear \x02address_index\x00\x02org\x00\x02primary\x00\x02goodkind-io\x00"'
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "writemode on; clear \x02address_index\x00\x02workspace\x00\x02primary\x00\x02main\x00"'
```

**4B (alternative, only if 4A is unsafe)**: write OLD UUIDs directly. The value is the raw 16-byte UUID. Org `019dc5ad-0408-7e43-9c4d-d3e6736ac058` becomes `\x01\x9d\xc5\xad\x04\x08~C\x9cM\xd3\xe6sj\xc0X`. Workspace `019dc5ad-0469-71e0-9e73-711bbcc0e93d` becomes `\x01\x9d\xc5\xad\x04iq\xe0\x9esq\x1b\xbc\xc0\xe9=`. This duplicates work that backfill does correctly. Prefer 4A.

Post-conditions for 4A:
```
ssh tack 'cd /root/tack && docker compose exec -T fdb fdbcli --exec "getrange \x02address_index\x00 \x02address_index\x01 100"'
```
This returns 0 rows.

Reversibility: yes. Phase 0's `address_index_pre.txt` is the rollback input.

### Phase 5: delete stale `org_members` row for NEW org

Pre-conditions:
- Phase 2-4 complete.
- Verify two rows still present and only the NEW org row will be deleted:
  ```bash
  ssh tack 'set -a; source /root/tack/.env; set +a; cd /root/tack && docker compose exec -T -e PGPASSWORD=$YUGABYTE_PASSWORD yugabyte ysqlsh -h yugabyte -p 5433 -U yugabyte -d tack -c "SELECT org_id, user_id, role FROM org_members WHERE user_id = '"'"'14385627-0313-50f8-bf7e-0c966e355dd9'"'"';"'
  ```

Affected SQL row: `org_members WHERE org_id = '3dc1c593-35ea-5214-a198-800e9f38916a' AND user_id = '14385627-0313-50f8-bf7e-0c966e355dd9'`.

Operator command:

```bash
ssh tack 'set -a; source /root/tack/.env; set +a; cd /root/tack && docker compose exec -T -e PGPASSWORD=$YUGABYTE_PASSWORD yugabyte ysqlsh -h yugabyte -p 5433 -U yugabyte -d tack -c "DELETE FROM org_members WHERE org_id = '"'"'3dc1c593-35ea-5214-a198-800e9f38916a'"'"' AND user_id = '"'"'14385627-0313-50f8-bf7e-0c966e355dd9'"'"';"'
```

Post-conditions: re-running the SELECT shows exactly one row (OLD org).

Reversibility: yes. `INSERT INTO org_members (org_id, user_id, role) VALUES ('3dc1c593-...', '14385627-...', 20)`.

### Phase 6: run `backfill.addresses.apply`

Pre-conditions:
- Phase 4A executed (both `address_index` rows for `goodkind-io` and `main` cleared). Otherwise apply will mark those rows `conflict` (per `backfill_addresses_plan.go:122-125`) and refuse to write (per `backfill_addresses_preview.go:82-89`).
- The deployed image is commit `dd430c9`, which has `internal/ops/backfill_addresses_preview.go`.

Dry-run first (read-only):

```bash
ssh tack 'cd /root/tack && docker compose exec app /server ops batch backfill.addresses.preview' | tee /tmp/backfill_preview.json
```

Verify the preview shows 9 candidates with `Status: write`, no `conflict`, no `malformed`. Each candidate's `Source.OwnerID` should be in OLD org.

Operator command (mutation):

```bash
ssh tack 'cd /root/tack && docker compose exec -e TACK_BACKFILL_APPLY=true app /server ops batch backfill.addresses.apply' | tee /tmp/backfill_apply.json
```

Post-conditions:
- `WrittenCount` in the JSON output equals 9 (1 org + 1 workspace + 7 projects).
- `ConflictCount = 0`, `MalformedCount = 0`.
- `getrange \x02address_index\x00 \x02address_index\x01 100` shows 9 entries, all pointing at OLD UUIDs.
- A re-run of `backfill.addresses.apply` shows `IdempotentCount = 9` and `WrittenCount = 0` (per `backfill_addresses_apply_test.go:33-44`, which asserts the rerun is idempotent).

Reversibility: yes. The 9 written rows can be cleared with `clear \x02address_index\x00\x02<type>\x00\x02primary\x00\x02<value>\x00`.

### Phase 7: validate via MCP read paths

Pre-conditions: all prior phases complete.

Read-only validations:

```bash
ssh tack 'cd /root/tack && docker compose exec app /server ops inspect query --org=019dc5ad-0408-7e43-9c4d-d3e6736ac058 --type=project --property=slug --value="\"tack\""'
```

Expect: 1 view returned, ID `019dc5ed-6825-79fb-a0c5-81 40813b00fb`.

Then exercise MCP `tack_list_workspaces` and `tack_list_projects` from a client to confirm the OLD org is now resolvable by `goodkind-io` (no `unknown reference strategy` error). Phase 1 rewrote the strategy on Org/Workspace/Project NodeTypes, which is what unblocks `Resolver.ResolveTypedNodeID` (`internal/adapters/mcp/tools/resolve_typed.go:42-53`).

Post-conditions: end-to-end MCP read against OLD workspace `main` and any project succeeds without strategy errors.

Reversibility: N/A.

## 4. Open questions answered

1. **Is workspace data under org's keyspace?** Yes. `node_instance` and `node_view` are keyed `(family, orgID, nodeType, nodeID)` (see `keys.go:111` and `keys.go:116`). A clearrange on `(family, NEW_ORG)` removes both org and workspace rows. **But** `node_resolve` is global, keyed only by `nodeID` (`keys.go:120-122`), so it needs explicit clears (Phase 3). Same for `address_index` (Phase 4).
2. **Cross-references from OLD to NEW?** None observed. The OLD org's instances and relationships are entirely self-contained under OLD_ORG prefix. No relationship rows under OLD point at NEW UUIDs (verified by enumerating relationship key prefixes).
3. **Did seed write any user-related records for NEW org?** No FDB membership records. Membership lives in SQL `org_members`, not FDB. Only the SQL row in Phase 5 needs cleanup. Audit emission was suppressed (verified: `count(*) = 0` in `audit.events` for NEW UUIDs).
4. **Does `backfill.addresses.apply` skip or overwrite?** It calls `WriteAddress`. That call invokes `ensureAddressOwner` (`node_address.go:108-141`). If the existing owner equals the row's owner, it is a no-op and returns `idempotent`. If it differs, it returns `ErrAlreadyExists`. The apply logic catches that error (`backfill_addresses_preview.go:104-112`) and converts it to a conflict that aborts the batch. Therefore Phase 4A (clearing the conflicting `address_index` rows first) is required.
5. **How many `node_type_definition` records under OLD org have `direct_slug`?** 3, listed in Section 2: `org`, `workspace`, `project` (full UUIDs and target JSON fields documented).
6. **Should `slug_index` be left alone?** Yes, leave it alone. It is read by `backfill.addresses.apply` as the source-of-truth for repopulating `address_index` (`internal/ops/backfill_addresses_preview.go:14-17`). Removing it now is unnecessary, and it removes the safety net for re-running the backfill.
7. **What does seed do on re-run after we finish?** After Phase 4A and Phase 6, `address_index` will hold `goodkind-io -> OLD_ORG` and `main -> OLD_WS`. Seed's `ensureNode` calls `GetAddress(typeKey, AddressKindPrimary, slug)` first (`cmd/server/seed.go:155`). Finding the OLD UUIDs, it returns and short-circuits (line 156-158). No new parallel org will be created. NodeType and PropertyDef seeding via `seeder.SeedOrg(ctx, orgID)` (line 110) is idempotent and re-runs against OLD_ORG, propagating any current type changes.

## 5. Risks and unknowns

- **Manual JSON rewrites in Phase 1 are fragile.** A small Go utility (a few dozen lines reading via `NodeStore.GetType` and writing via `WriteType`, gated behind a `--dry-run` flag) is the safer path than hand-built `fdbcli set` commands. If we go with hand `fdbcli`, double-check the entire JSON byte-for-byte against the Phase 0 snapshot before commit. Risk: byte-corrupting the value blocks seed and runtime resolve.
- **Concurrent traffic during remediation.** This playbook assumes the operator runs it during a quiet window. If MCP traffic is live, putting the deploy into single-user mode (or briefly stopping the `app` service before Phase 2-5) reduces interference. Validation in Phase 7 should happen after `app` is back up.
- **`WriteAddress` is idempotent for same owner, conflicting for different owner.** If a future `backfill.addresses.apply` finds an entry that is not in the legacy snapshot, that conflict will block the batch. This does not affect the current playbook, but operators running future backfills should re-snapshot first.
- **Other `direct_slug` records elsewhere?** I scanned only OLD org and NEW org NodeType records. There is no other org in production today (verified: `getrangekeys \x02node_type_def\x00 \x02node_type_def\x01` shows only the two org IDs). If a third org appears later with `direct_slug`, the same Phase 1 procedure applies.
- **`node_address_by_node` reverse index.** CLAUDE.md mentions one, but the current code base has only the forward `address_index`. No reverse-index cleanup is needed. If a future commit adds a reverse index, this playbook will need a corresponding cleanup step.

## 6. Estimated total operator time

- Phase 0: 5 minutes
- Phase 1 (build + verify Go utility, run, verify): 10-15 minutes
- Phase 2: 5 minutes (9 clearranges)
- Phase 3: 1 minute (2 clears)
- Phase 4A: 1 minute (2 clears)
- Phase 5: 2 minutes (1 SQL DELETE + verify)
- Phase 6: 5 minutes (preview + apply + read-back)
- Phase 7: 5 minutes (MCP smoke tests)

Subtotal: ~30-40 minutes. Add 5-10 minutes of buffer for SSH latency and verification reads. **Total: 30-45 minutes**, with three natural checkpoints (after Phase 1, after Phase 4, after Phase 6) where progress can be paused safely.
