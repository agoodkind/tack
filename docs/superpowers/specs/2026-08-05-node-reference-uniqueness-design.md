# Node reference uniqueness

## Summary

The existing reader derives a human-readable reference such as `FAN-13` from two
properties on every read without recording which node claims the reference. The
writer does not reject duplicate references, and the resolver returns its first
match. A second node with the same reference cannot use that address.

Declare uniqueness as NodeType metadata and enforce it in the FoundationDB write
transaction. One declaration renders and parses the reference and defines its
uniqueness and lookup keys. A sequence counter populates a property covered by
that constraint rather than requiring separate reference rules.

## The engine and one instance of its data

The engine renders references and enforces constraints from templates in NodeType
metadata. It does not define a reference shape. A NodeType without a template does
not impose a constraint or render a reference. The engine must not supply a
built-in template, implicit part, or fallback.

`FAN-13`, `TACK-26`, the hyphen separator, the `sequence` property, and project
scope are examples from one deployment, not engine requirements. Another
deployment can declare different types, separators, generated properties, and
scopes without changing the engine's template interpretation.

## Problem

`FAN-13` identifies two issues in the FAN project. `tack_get_issue` with
`node_id=FAN-13` returns one. MCP tools resolve references the same way and cannot
read, update, or transition the other issue by that reference.

State updates for four intended issues changed four different issues to Done
and reported success instead of an error. TACK-342 (`Duplicate sequence numbers
make issues unaddressable through MCP tools`, high, Todo) records the incident.

Eight references in FAN identify two nodes each: 4, 6, 7, 8, 13, 14, 15, and 16.
`tack_list_issues` for FAN returns 40 issues across overlapping sequence ranges.
TACK-342 documents four of the eight duplicates.

### Why it happens

The writer allocates a sequence once and stores it as a plain property without
binding it to the allocation scope. Duplicates arise through two operations.

#### Type-scoped counter, type-agnostic reference

`sequenceKey` in `internal/adapters/foundationdb/keys.go` keys the counter by
`(orgID, scopeNodeID, nodeType)`. `referenceIdentifierFor` in
`internal/adapters/mcp/tools/render_scope.go` renders
`<scopeIdentifier>-<sequence>` without the type. Every NodeType declaring
`ReferenceScopedSequence` under one scope starts its own counter at 1 but uses
the same reference namespace. Epic and issue `FAN-1` both exist, as do `FAN-2`
and `FAN-5`.

Each user-defined type with this strategy adds another potentially colliding
counter without a fixed upper bound.

#### Scope rewritten without renumbering

`repair.sequence_scope_ids` in `internal/ops/repair_sequence_scope_ids.go`
derives and writes canonical `scope_id` from the parent chain without reading
or writing `sequence`. Moving a node into a scope that already issued its number
creates a collision. The destination counter cannot detect a number allocated
elsewhere.

### Why resolution cannot catch it

`resolveSequenceNodeID` in `internal/adapters/mcp/tools/resolve_typed.go` scans
the `sequence` property index, filters candidates in Go by walking the parent
chain, and returns the first match. Other reference strategies use `uniqueMatch`
in `internal/adapters/mcp/tools/resolve_reference_helpers.go` lines 99 to 109,
which returns `matched multiple nodes` for an ambiguous lookup. Only the sequence
strategy selects one ambiguous candidate.

`keys.go` declares ten key families without a reference-to-node key that could
enforce uniqueness. `node_by_property` uses
`(orgID, nodeType, propName, encodedValue, nodeID)` without a scope component.
`sequence` stores only the counter.

## Goals

The engine must satisfy these requirements:

- The storage layer guarantees that every rendered reference resolves to exactly
  one node at write time, for any operator-declared template.
- The operator declares the uniqueness domain as data. The engine must not infer
  it from hardcoded type or property names or built-in templates. The seed
  supplies an editable starting suggestion, not a default.
- A reference lookup requires one point read.
- Templates can declare independent numbering for distinct node kinds and render
  distinguishable references, or declare shared numbering. Neither strategy has
  special treatment.
- Detect and repair operations interpret each deployment's templates.

For this deployment, those operations must repair the eight FAN duplicates and
audit every project in every org for the same condition.

## Non-goals

- Do not change the reference format the seed writes. `FAN-13` keeps its shape;
  only repaired nodes change reference values.
- Do not add forwarding records from old references to new ones. The repair
  section explains this decision. TACK-365 (`Optional reference forwarding for
  nodes that move between scopes`, low, Todo) tracks forwarding separately.
- Do not add reference history beyond the repair operation's ledger entry.
- Keep UUIDs as canonical identity. References provide addresses, not identity.

## Verified facts

The source review established these facts:

- `sequenceKey(orgID, scopeNodeID, nodeType)` at `keys.go` lines 135 to 137
  includes `nodeType`, although the comment at lines 43 to 46 states
  `scopeNodeID is the container that defines uniqueness`.
- The ten key families at `keys.go` lines 16 to 60 do not map references to nodes.
- `referenceIdentifierFor` in `render_scope.go` formats `%s-%d` from the scope
  identifier and `nodeType.Reference.Property`, omitting the type component.
- `resolveSequenceNodeID` in `resolve_typed.go` returns the first candidate
  satisfying `nodeBelongsToScope` and passes literal `"sequence"` to
  `ListByProperty` instead of `nt.Reference.Property`.
- `allocateCreateSequence` in `internal/service/node_create_prepare.go` writes
  literal `props["sequence"]`, also ignoring `nt.Reference.Property`.
- `uniqueMatch` at `resolve_reference_helpers.go` lines 99 to 109 returns
  `ErrInvalidArgument` for multiple matches. `resolveDirectNodeUnderParent` in
  `resolve_scope_reference.go` uses this helper; the sequence path does not.
- `PropertyDef` at `internal/domain/node/types.go` lines 254 to 274 declares
  storage and validation requirements through `Indexed` (line 263), `Required`
  (line 265), and `AppliesToFeatures` (line 261). The comment at lines 252 to 253
  states `PropertyDefs are never scoped by hardcoded NodeType name lists`.
- `ReferenceConfig{Strategy, Property}` at `types.go` lines 117 to 120 supports
  `uuid_only`, `scoped_sequence`, and `scoped_property` (lines 109 to 113), plus
  `direct_property` in `internal/domain/node/reference.go` line 9.
- `FeatureIsScope` at `types.go` line 38 means `defines an FDB key scope level`.
- `Set` (line 71) and `UpdateAtomic` (line 96) share `writeNodeRecords` in
  `internal/adapters/foundationdb/node.go` line 191. `CreateAtomic` at line 302
  duplicates the primary, resolve, and view writes at lines 328 to 352 instead
  of calling `writeNodeRecords`. `Delete` at line 220 clears those records and
  walks `n.Props` to clear property indexes. Its comment at lines 286 to 288
  notes the absence of a per-node reverse index.
- `NodeRepository` in `internal/domain/node/repository.go` lines 84 to 86
  requires the caller to resolve `indexedProps` from the PropertyDef registry.
  The storage layer does not read PropertyDefs.
- `NodeService.Create` in `internal/service/node_create.go` line 97 calls
  `s.indexedPropNames` and passes the result to `createNodeAtomic`. Reference
  keys use the same service-to-storage contract.
- `ParseNodeIdentifier` in `internal/adapters/mcp/tools/resolve_parse.go` splits
  on the last hyphen and requires a positive integer suffix, hardcoding
  `PROJECT-N`.
- `reconcilePropertyIndexes` at `node.go` line 108 clears each changed indexed
  property's old entry and writes its new entry in the same transaction.
- Relationships use UUID keys at `keys.go` lines 125 to 132. Container, parent,
  and workflow-state pointers use UUID-valued properties. Internal records do
  not address nodes by rendered reference.
- `PropertyDefaultReference.Reference` in
  `internal/domain/node/default_reference.go` lines 15 to 20 stores a string
  resolved at create time, not a UUID. `resolveCreateDefaultReference` in
  `internal/service/node_defaults.go` accepts only `ReferenceScopedProperty`
  target strategies. `resolveDefaultScopedPropertyReference` matches children
  of the create scope by property value. A default stores a scope-local value
  such as `Todo`, not a `scoped_sequence` reference such as `FAN-13`. The system
  does not persist rendered sequence references.
- `uniqueDefaultReferenceMatch` in `node_defaults.go` returns
  `matched multiple nodes` for ambiguous defaults. It also rejects ambiguity,
  like `uniqueMatch`. Only sequence resolution selects one ambiguous candidate.
- `ops.Register(Operation{Name, Description, Run})` in `internal/ops/ops.go`
  lines 39 to 43 and 88 to 99 registers maintenance operations.
- New entities use time-sortable UUIDv7 per `AGENTS.md`.
- Rendered list and search output omits raw cross-reference UUIDs.
  `internal/adapters/mcp/tools/render_test.go` asserts this behavior.

## Design

### The declaration

A NodeType declares an ordered list of reference key templates. Each template
has a NodeType-local unique name for storage keys and errors, plus an ordered
list of parts that produces one string. Exactly one template serves as the
human-facing reference.

Each template renders and parses references and defines both uniqueness and
lookup keys. A per-property Boolean cannot express the composite scope identifier
and number. Multiple templates support additional constraints, such as a unique
external tracker identifier alongside the human-readable reference.

### Part kinds

The template supports four part kinds:

| Part kind | Renders |
| --- | --- |
| `scope_ref` | The rendered reference of the nearest ancestor declaring a named feature. Produces `FAN`. |
| `property` | The value of a named property on the node. Produces `13` from a counter property, or `In Progress` from a name property. |
| `node_type` | The node's own `TypeKey`. Present only when an operator wants each kind numbered and addressed separately. |
| `literal` | Fixed text used as a prefix or separator. |

`scope_ref` selects its domain by feature, not type key, matching
`PropertyDef.AppliesToFeatures`. The seed uses `FeatureIsScope`.

The current strategies correspond to these templates:

| Strategy | Template |
| --- | --- |
| `scoped_sequence` | `scope_ref(is_scope)`, `literal("-")`, `property(sequence)` |
| `scoped_property` | `scope_ref(is_scope)`, `literal("::")`, `property(name)` |
| `direct_property` | `property(identifier)` |
| `uuid_only` | Empty. No reference, no constraint. |

### The counter derivation rule

A value generator derives its key from the template except the part it fills.
For a counter filling `property(sequence)` in
`scope_ref(is_scope), literal("-"), property(sequence)`, the resolved scope node
is the counter key. A template without `node_type` omits it from the counter key;
a template with `node_type` includes it. The declaration determines numbering,
lookup, and rendered reference together.

The new derivation changes key encoding, and an unseen counter starts at zero.
Before creating nodes with the new keys, the repair pass seeds each counter to
the highest value it observes in that scope. Otherwise, counters reissue values
already in use.

### Enforcement

The forward and reverse key families enforce the constraint:

```
(node_reference,       orgID, templateName, encodedKey) -> nodeID
(node_reference_owned, orgID, nodeID, templateName)     -> encodedKey
```

`encodedKey` consists of tuple-packed rendered parts. The forward family enforces
uniqueness and answers lookups. The reverse family locates a node's prior keys
by identifier for `Delete` and `UpdateAtomic`, which cannot render keys without
NodeType metadata. `relationship_reverse` serves the same purpose for
relationships. The `Delete` comment notes the missing per-node reverse index for
property values. Both new families interpret metadata without special cases for
concrete node types.

`CreateAtomic` writes primary, resolve, and view records directly. `Set` and
`UpdateAtomic` share `writeNodeRecords`. `Delete` clears node records. Each path
writes or clears reference keys in its existing transaction. Primary records,
views, resolve records, property indexes, and reference keys commit or fail
together.

The write transaction first reads the forward key. If it identifies another
node, the transaction fails with a conflict error identifying both nodes.
An existing key for the same node does not require a change.

On update, the transaction reads prior keys from the reverse range, clears the
old forward keys, and writes new keys, like `reconcilePropertyIndexes`.
Moving a node into a scope with a conflicting rendered reference fails.

The service renders keys and supplies them to storage, as it supplies
`indexedProps`. The storage layer must not read PropertyDefs or NodeTypes under
the `NodeRepository` contract.

### Resolution

`resolveSequenceNodeID` uses a point read on the reference key. Parsing uses the
same template as rendering. Resolution must not scan `node_by_property`, walk
parent chains for candidates in Go, or loop over sequence-bearing types per
workspace.

A node created before reference keys existed can miss the point read. The
resolver then uses the existing scan with `uniqueMatch`, which rejects duplicates
with `matched multiple nodes` and both UUIDs. Remove this fallback after repair
reports zero duplicates across every org.

### What the seed writes

The engine must not supply a built-in or fallback template or implicit part.
A NodeType without a template does not impose a constraint or render a reference.

The seed supplies an editable, removable starting suggestion. For `issue`,
`epic`, `cycle`, and `module`, it writes
`scope_ref(is_scope), literal("-"), property(sequence)` without `node_type`.
`FAN-13` identifies exactly one node across work-item types. This template
invalidates the epic/issue pairs sharing `FAN-1`, `FAN-2`, and `FAN-5`; repair
reassigns one node in each pair.

TACK has the same collision across more references. Its epic counter is 26 and
its issue counter exceeds 369. Each epic reference from `TACK-1` to `TACK-26`
also identifies an issue. Repair reassigns epics because issues are older and the
retention policy keeps the older node. On 2026-08-05, `tack_get_epic` and
`tack_get_issue` each resolved `TACK-26` correctly using their own type keys.
`tack_list_relationships` resolved it to the epic. Untyped relationship tools
could address only that member of the pair.

A deployment can include `node_type` for independent numbering per type.
The nodes then use separate counters and distinguishable references. The engine
enforces both template shapes without special treatment.

## Repair

### Detect

Register a maintenance operation through `ops.Register`. For every org and
NodeType with a template, report each duplicated rendered reference with all
candidate UUIDs and creation times.

Detection is a read operation and follows the [audit policy](../../audit.md).
The first run establishes whether projects beyond FAN contain duplicates; the
eight confirmed duplicates are all in FAN.

### Renumber

Renumbering changes references that people may have written down. Print the
complete old-to-new mapping and record it in the audit ledger before applying
changes.

Group nodes by rendered reference. Every node except the retained one receives
a fresh counter value above the scope's highest allocated value.

The operation selects which node to retain; storage enforcement only rejects
duplicates. `--keep` accepts `oldest` or `newest` and defaults to `oldest`.
`oldest` orders by time-sortable UUIDv7 without additional stored data. A stale
reference continues to identify the older node, the more probable target of
older text.

Write a reference key for every visited node in the same pass to repair
duplicates and populate keys for nodes created before templates existed.
Renumbering commits each change and its audit evidence in one FoundationDB
transaction.

### No forwarding record

A forwarding record maps one prior reference to one successor node. A duplicated
reference identifies two nodes without a rule for selecting a successor.
Forwarding would recreate the ambiguity this design removes.

Moving a single node between scopes differs: its prior reference identifies one
node and one successor. TACK-365 (`Optional reference forwarding for nodes that
move between scopes`, low, Todo) tracks that optional feature outside this repair.

After write-time enforcement, duplicate-driven renames occur only during the
one-time repair and do not require a permanent data structure. The ledger entry
provides the recovery mapping for a stale reference. Internal records use UUIDs,
not rendered references; renames do not change relationships, comments, activity,
workflow-state pointers, or audit rows.

## Testing

Reproduce defects through production code paths.

### Reproduce cross-type duplicates

Create a project, an epic, and an issue with ordinary create calls. Both work
items receive sequence 1 and render the same reference.

### Reproduce same-type duplicates

Create issues under separate scopes, then run `repair.sequence_scope_ids`.
It rewrites `scope_id` without changing `sequence`. The destination scope has
already issued the moved node's number.

### Verify detect and renumber

Assert that detect reports both duplicate groups with their UUIDs. Run renumber
with the execute gate and compare its printed mapping with the ledger entry.

### Verify repaired references

Assert that each reference resolves to exactly one node and that a fresh create
in the repaired project receives a value above the prior maximum. Repeating the
cross-type reproduction must fail with an invalid-argument error instead of
creating a duplicate.

### Verify template interpretation

A NodeType declaring `property(number)` must allocate, render, and resolve
`number` without reading literal `sequence`. A template including `node_type`
must allow an epic and issue with the same number and render distinct references.

Unit tests verify rendering, parsing, and counter-key derivation without a
database. `make test-integration` runs integration tests with single-node
FoundationDB and YugabyteDB containers.

## Rollout

1. Add the template declaration, reference key family, and shared-write
   enforcement with the scan fallback. Preserve non-duplicate reference behavior.
2. Add detect and run it read-only against QA and production to establish the
   affected projects.
3. Add renumber and validate it on disposable QA recreated to match production,
   per `AGENTS.md`.
4. Run renumber against production with the execute gate and record the mapping
   in the ticket.
5. Seed templates for built-in work-item types and rerun renumber to resolve the
   cross-type collisions those templates expose.
6. Remove the scan fallback after detect reports zero duplicates across all orgs.

## Work breakdown

Create one epic with these tasks:

1. Add reference key template types to `NodeType`, with unit-tested rendering,
   parsing, and counter-key derivation, without storage changes.
2. Add the `node_reference` family and enforcement in `writeNodeRecords`,
   including clearing prior keys on update.
3. Add point-read resolution with the scan fallback using `uniqueMatch`.
4. Replace literal `"sequence"` in `allocateCreateSequence` and
   `resolveSequenceNodeID` with the template-declared property.
5. Register the read-class detect operation through `ops.Register`.
6. Audit every project in every org for duplicate references using detect and
   record results on TACK-342.
7. Add atomic renumber with `--keep`.
8. Seed templates for `issue`, `epic`, `cycle`, and `module`.
9. Add integration regressions for both reproductions and post-repair assertions.
10. Remove the scan fallback.

TACK-342 records four duplicate references. The verified FAN count is eight.

## Open questions

The design does not have blocking questions. The repair section explains the
decision against forwarding records.
