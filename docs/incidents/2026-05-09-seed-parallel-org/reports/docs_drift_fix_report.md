# Docs Drift Fix Report

Date: 2026-05-09
Scope: align project documentation with code at commit `dd430c9`.

Note: `CLAUDE.md` is a symlink to `AGENTS.md` in this repo; the symlink is
the documented project doc, the symlink target is the file the edits touched.
The file paths below reference both forms where useful.

## 1. Files changed

- `/Users/agoodkind/Sites/tack/AGENTS.md` (target of the `CLAUDE.md`
  symlink): replaced the `node_address` / `node_address_by_node` block in
  the FDB key space section with the actual `address_index` key, and added
  a new `Deprecated names in older artifacts` section between the SQL
  schema section and the FDB key space section.

No other files were modified. `state_audit_full_impact.md` was left intact
as a historical artifact (it predates the repair refactor); the cross
reference now lives in the project doc.

## 2. Drifts fixed inline

### Drift 1: address index key shape

Before, the `Address/reference indexes` block read:

```
node_address          orgID, scopeID, nodeType, addressKind, addressValue -> nodeID
node_address_by_node  orgID, nodeID, addressKind, scopeID, addressValue   -> nil
```

After, it reads:

```
address_index         nodeType, addressKind, address -> nodeID
```

A short prose note follows the code block. The note states that the index
is global, points at `internal/adapters/foundationdb/keys.go` (the
`addressIndexKey` constructor on lines 140 to 142, with the `keyAddressIndex`
constant on line 46), records that there is no `node_address_by_node`
reverse index in the code base, and references
`incident_2026-05-09_seed_parallel_org/retro_log.md` section 1B for the
global-vs-scoped tradeoffs and the missing-reverse-index follow-up. The
note flags both items as open questions without implying a decision.

### Drift 2: repair class names

The `state_audit_full_impact.md` document at the repo root references a
`stray_alias_state` repair class and a `repair_stray_alias_state.go` file
that no longer exist. The current registered classes (from
`internal/ops/repair_catalog.go`) are `reference_property`,
`parent_reference`, and `props_transform`.

Per the task constraint, `state_audit_full_impact.md` is treated as a
historical artifact (dated 2026-05-05, predating the refactor) and was
left unchanged. The forward-looking equivalent is now documented in the
new `Deprecated names in older artifacts` section of `AGENTS.md`. The
mapping recorded there is:

- `stray_alias_state` resolvable-raw-alias and rank-winner cases map to
  `reference_property` (with operator-declared source fields, scope, and
  conflict policy; a value-map normalization expresses the legacy
  `open` / `in_progress` / `in-progress` aliases when an explicit policy
  for them is decided).
- `stray_alias_state` "delete the stale raw `state` alias when the
  canonical `state_id` is already valid" cleanup maps to
  `props_transform` (delete or rename of the raw `state` property).
- `parent_reference` is the new third class; it has no analog in the
  removed `stray_alias_state` path and is listed for completeness.

The mapping is consistent with what `state_repair_execution_report.md`
already records under "Toolchain reconciliation" (sections 57 and 111).

## 3. Drifts flagged for operator decision

### Address index design

The address index is global in code, scoped in the prior doc. The new doc
language matches the code, but the underlying design question is a
decision for the operator. Options are global as today, fully scoped, or
hybrid (org references global, everything else scoped). Captured in
retro_log.md section 1B; not decided here.

### Reverse address index

`node_address_by_node` is described in the prior doc but absent from the
code. The new doc records the absence. Whether to implement the reverse
index, and at what scope, depends on the address-index design decision
above. Not decided here.

### Repair-class history in `state_audit_full_impact.md`

The historical audit doc still names `stray_alias_state`. Editing that
file would rewrite a dated audit artifact. The cleaner alternative
(adopted here) is to leave it alone and add the deprecation/mapping note
to `AGENTS.md`. If the operator prefers an in-place note inside the
audit doc, that is a quick follow-up edit; flagging rather than acting
on it.

### Audit table existence

Retro section 1D notes that `audit.events` may be missing in production.
That is explicitly out of scope per the task instructions. Not
investigated; not edited.

## 4. Additional drifts surfaced beyond drifts 1 to 3

Several other key families documented in `AGENTS.md` do not match the
constants in `internal/adapters/foundationdb/keys.go`. These are flagged
rather than fixed inline because the discrepancy is large enough that
the operator should choose between rewriting the doc to match the
generic-relationship model in code, or formally documenting that the
doc enumerates "intended" key shapes and the code uses generic
relationships to represent them.

The mismatches I observed:

- `node_list_view` (doc) vs `node_view` (code, `keyNodeView`).
- Doc `node_list_view` is keyed by `(orgID, workspaceID, nodeType, nodeID)`;
  code packs `(orgID, nodeType, nodeID)`.
- `node_instance` (doc) is keyed by `(orgID, workspaceID, nodeType, nodeID)`;
  code packs `(orgID, nodeType, nodeID)`.
- Doc lists `node_instance_by_project` and `node_instance_by_state`
  secondary indexes; neither exists in `keys.go`.
- `node_by_property` (doc) keys include `propDefID`; code uses `propName`.
  Doc also includes `workspaceID`; code does not.
- `relation_from_node` / `relation_to_node` (doc) vs `relationship` /
  `relationship_reverse` (code).
- `sequence` (doc) keys are `(orgID, scopeType, scopeID, nodeType)`; code
  packs `(orgID, scopeNodeID, nodeType)` with no `scopeType`.
- `node_type_definition` / `property_definition` (doc) vs `node_type_def` /
  `property_def` (code).
- `idempotency_key` exists in code but is not documented.
- All of the specialized families documented under Assignments, Labels on
  nodes, Containment, Hierarchy, Comments, Activity log, Membership,
  Watchers and mentions, Notifications, Counters, Positioning and views,
  Content, Work tracking, Custom fields (`property_value_on_node`),
  Automation and rules, Settings and roles, and Integrations and ops do
  not have corresponding key constants in `keys.go`. The code uses the
  generic `relationship` / `relationship_reverse` and `node_by_property`
  families instead. This is consistent with the "everything is a node,
  generic relationships" principle in the doc, but the doc still lists
  the specialized families in detail.

These drifts are surfaced here for operator decision rather than fixed
inline because reconciling them implies a design choice about whether
the FDB key space section enumerates implementation keys or intended
logical access patterns.

## 5. Verification

After each edit, the relevant section was re-read and the result
matches the target. Quick read-back:

- `AGENTS.md` lines 159 to 176 contain the new `Deprecated names in
  older artifacts` section, naming `reference_property`,
  `parent_reference`, and `props_transform` with the mapping from
  `stray_alias_state`, and citing `state_repair_execution_report.md`.
- `AGENTS.md` lines 189 to 204 contain the new
  `Address/reference indexes` block, with the `address_index` key
  shape, the global-vs-scoped note, and the reference to retro section
  1B.
- A scan of `AGENTS.md` for em-dash (U+2014) and en-dash (U+2013) via
  `python3 /tmp/scan_em.py` returns `total_hits=0`.
- Source-of-truth checks: `internal/adapters/foundationdb/keys.go`
  `keyAddressIndex` is `"address_index"` (line 46) and
  `addressIndexKey` packs `(keyAddressIndex, nodeType, string(addressKind), address)`
  (lines 140 to 142). `internal/ops/repair_catalog.go` registers
  exactly the three classes named in the new doc section.
