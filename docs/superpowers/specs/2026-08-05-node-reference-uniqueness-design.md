# Node reference uniqueness

## Summary

A human-readable node reference such as `FAN-13` is a derived string, recomputed
on every read from two properties, with no stored record that the name is taken.
Nothing refuses to issue the same reference twice and nothing detects that it
already has. Two nodes can therefore share a reference, and the resolver returns
whichever it encounters first while the other node becomes unreachable by name.

This design makes uniqueness a constraint declared as data on a NodeType,
enforced inside the same FoundationDB transaction as the write. One declaration
serves four purposes at once: it renders the reference, parses the reference,
defines the uniqueness key, and is the lookup index. Because there is one
declaration, the printed name and the guarantee cannot disagree.

A sequence counter becomes one way to populate a property that a constraint
covers, not a special case with its own rules.

## The engine and one instance of its data

The engine holds no reference shape. It reads templates from NodeType metadata
and does what they say. A NodeType with no template carries no constraint and
renders no reference. There is no built-in template, no implicit part, no
fallback, and no code path that supplies one.

Everything concrete in this document, `FAN-13`, `TACK-26`, a hyphen separator, a
property named `sequence`, a project as the enclosing scope, is data from one
deployment. Another deployment declares different types, different separators,
different generated properties, and a different idea of what a scope is, and the
engine behaves identically.

Read every named reference below as an observation of one board, used as
evidence that the engine currently permits a collision. None of it is a
requirement on the engine.

## Problem

The engine permits two nodes to render the same reference. The section below
evidences that with one deployment's data.

`FAN-13` names two issues in the FAN project. `tack_get_issue` with
`node_id=FAN-13` returns one of them. The other cannot be read, updated, or
transitioned through any MCP tool, because every tool path that accepts a
reference resolves it the same way.

The failure is a silent wrong-target write, not an error. Setting state on four
issues moved four different issues to Done and reported success. TACK-342
(`Duplicate sequence numbers make issues unaddressable through MCP tools`, high,
Todo) records that incident.

Eight references in the FAN project name two nodes each: 4, 6, 7, 8, 13, 14, 15,
and 16. `tack_list_issues` for project FAN returns 40 issues across two
overlapping runs of sequence numbers. TACK-342 documents four of the eight.

### Why it happens

Two mechanisms produce duplicates. Both exploit the same gap: a sequence number
is allocated once, stored as a plain property, and never bound to the scope it
was allocated from.

**Type-scoped counter, type-agnostic reference.** `sequenceKey` in
`internal/adapters/foundationdb/keys.go` keys the counter by
`(orgID, scopeNodeID, nodeType)`. `referenceIdentifierFor` in
`internal/adapters/mcp/tools/render_scope.go` renders
`<scopeIdentifier>-<sequence>` and omits the type. Every NodeType declaring
`ReferenceScopedSequence` under one scope runs its own counter from 1, and all of
them render into one flat namespace. Epic `FAN-1` and issue `FAN-1` both exist,
as do `FAN-2` and `FAN-5`.

Node types are user-extensible, so the count of colliding counters is unbounded
and grows whenever an operator adds a type declaring that strategy.

**Scope rewritten without renumbering.** The `repair.sequence_scope_ids`
operation in `internal/ops/repair_sequence_scope_ids.go` derives a canonical
`scope_id` from the parent chain and writes it. It never reads or writes
`sequence`. A node moved into a scope whose counter already issued its number
collides, and the counter cannot detect it because the number was issued
elsewhere.

### Why resolution cannot catch it

`resolveSequenceNodeID` in `internal/adapters/mcp/tools/resolve_typed.go` scans
the `sequence` property index, then filters candidates in Go by walking the
parent chain, and returns the first match. Every other reference strategy funnels
through `uniqueMatch` (`internal/adapters/mcp/tools/resolve_reference_helpers.go`
lines 99 to 109), which returns `matched multiple nodes` when a lookup is
ambiguous. The sequence path is the only strategy that resolves ambiguity by
picking one.

Uniqueness is unenforceable in the current shape because no key exists to collide
on. `keys.go` declares ten key families. `node_by_property` is keyed
`(orgID, nodeType, propName, encodedValue, nodeID)` with no scope component.
`sequence` holds only the counter. Nothing maps a reference to a node.

## Goals

Engine goals:

- A rendered reference resolves to exactly one node, guaranteed by the storage
  layer at write time, for any template any operator declares.
- The uniqueness domain is declared as data by the operator. It is never
  inferred from a hardcoded type name, a hardcoded property name, or a built-in
  template. The seed writes a starting suggestion, not a default.
- Resolving a reference is one point read.
- An operator who wants two kinds of node to number independently declares it in
  the template, and the rendered reference distinguishes them. An operator who
  wants them to share one pool declares that instead. Neither is privileged.
- A detect operation and a repair operation work against whatever templates a
  deployment declares.

Instance goals for this deployment, achieved by running those operations:

- The eight duplicated references in the FAN project are repaired.
- Every project in every org is audited for the same condition.

## Non-goals

- Changing the reference format the seed writes. `FAN-13` keeps its shape.
  Individual values change only for the nodes the repair reassigns.
- A forwarding record from an old reference to a new one. See the repair
  section, and TACK-365 (`Optional reference forwarding for nodes that move
  between scopes`, low, Todo).
- Reference history or an audit of reference changes beyond the ledger entry the
  repair operation already writes.
- Changing the UUID as canonical identity. References are addressing, not
  identity.

## Verified facts

Each was read from the source named.

- `sequenceKey(orgID, scopeNodeID, nodeType)` at `keys.go` lines 135 to 137. The
  comment at lines 43 to 46 states `scopeNodeID is the container that defines
  uniqueness`, while the packed key also includes `nodeType`.
- `keys.go` declares ten key families at lines 16 to 60. None maps a reference to
  a node.
- `referenceIdentifierFor` in `render_scope.go` formats `%s-%d` from the scope
  identifier and `nodeType.Reference.Property`, with no type component.
- `resolveSequenceNodeID` in `resolve_typed.go` returns the first candidate
  satisfying `nodeBelongsToScope`, and passes the string literal `"sequence"` to
  `ListByProperty` rather than `nt.Reference.Property`.
- `allocateCreateSequence` in `internal/service/node_create_prepare.go` writes the
  string literal `props["sequence"]`, also ignoring `nt.Reference.Property`.
- `uniqueMatch` at `resolve_reference_helpers.go` lines 99 to 109 returns
  `ErrInvalidArgument` for more than one match. `resolveDirectNodeUnderParent` in
  `resolve_scope_reference.go` uses it. The sequence path does not.
- `PropertyDef` at `internal/domain/node/types.go` lines 254 to 274 already
  carries storage and validation obligations as data: `Indexed` (line 263),
  `Required` (line 265), and `AppliesToFeatures` (line 261). The doc comment at
  lines 252 to 253 states `PropertyDefs are never scoped by hardcoded NodeType
  name lists`.
- `ReferenceConfig{Strategy, Property}` at `types.go` lines 117 to 120. Strategies
  are `uuid_only`, `scoped_sequence`, `scoped_property` (`types.go` lines 109 to
  113) and `direct_property` (`internal/domain/node/reference.go` line 9).
- `FeatureIsScope` at `types.go` line 38 means `defines an FDB key scope level`.
- `writeNodeRecords` at `internal/adapters/foundationdb/node.go` line 191 is
  shared by `Set` (line 71) and `UpdateAtomic` (line 96). `CreateAtomic` at line
  302 does not call it and inlines the same primary, resolve, and view writes at
  lines 328 to 352. `Delete` at line 220 clears them and walks `n.Props` to clear
  property indexes, noting at lines 286 to 288 that no per-node reverse index
  exists.
- `NodeRepository` at `internal/domain/node/repository.go` lines 84 to 86 states
  that the caller resolves `indexedProps` from the PropertyDef registry and that
  the storage layer does not read PropertyDefs.
- `NodeService.Create` at `internal/service/node_create.go` line 97 calls
  `s.indexedPropNames` and passes the result to `createNodeAtomic`. Reference
  keys follow the same path.
- `ParseNodeIdentifier` at `internal/adapters/mcp/tools/resolve_parse.go` splits
  on the last hyphen and requires a positive integer suffix. It is hardcoded to
  the one shape `PROJECT-N`.
- `reconcilePropertyIndexes` at `node.go` line 108 clears the old index entry and
  writes the new one for each changed indexed property, inside the same
  transaction.
- Relationships are keyed by UUID at `keys.go` lines 125 to 132. Container,
  parent, and workflow-state pointers are UUID-valued properties. No internal
  record addresses a node by its rendered reference.
- `PropertyDefaultReference.Reference` at
  `internal/domain/node/default_reference.go` lines 15 to 20 stores a string
  resolved at create time, not a UUID. `resolveCreateDefaultReference` in
  `internal/service/node_defaults.go` rejects any target type whose reference
  strategy is not `ReferenceScopedProperty`, and
  `resolveDefaultScopedPropertyReference` matches it against children of the
  create scope by property value. A default therefore holds a scope-local value
  such as `Todo` and can never hold a `scoped_sequence` reference such as
  `FAN-13`. No rendered sequence reference is persisted anywhere.
- `uniqueDefaultReferenceMatch` in `node_defaults.go` returns
  `matched multiple nodes` for an ambiguous default. It is the third
  fail-loud-on-ambiguity helper in the codebase, alongside `uniqueMatch`. The
  sequence resolution path is the only lookup that resolves ambiguity by
  selecting one candidate.
- `ops.Register(Operation{Name, Description, Run})` at `internal/ops/ops.go`
  lines 39 to 43 and 88 to 99 is the maintenance-operation registry.
- New entities use UUIDv7, which is time-sortable, per the durable invariants in
  `AGENTS.md`.
- Rendered list and search output omits raw cross-reference UUIDs by design.
  `internal/adapters/mcp/tools/render_test.go` asserts that no cross-reference
  UUID appears in a rendered body.

## Design

### The declaration

A NodeType carries an ordered list of reference key templates. Each template has
a name, unique within its NodeType, which identifies it in the storage key and in
error messages. Each template declares an ordered list of parts and produces one
string. Exactly one template is marked as the human-facing reference.

A template is the whole contract. It renders the reference, parses an incoming
reference, forms the uniqueness key, and forms the index key. There is one
declaration, so those four cannot drift apart.

A per-property boolean flag is insufficient. It expresses only single-value
uniqueness, and the reference is already a composite of a scope identifier and a
number. A list of templates on the NodeType also supports a type needing more
than one guarantee, such as a unique reference alongside a unique external
tracker identifier.

### Part kinds

Four part kinds express every reference in the product.

| Part kind | Renders |
| --- | --- |
| `scope_ref` | The rendered reference of the nearest ancestor declaring a named feature. Produces `FAN`. |
| `property` | The value of a named property on the node. Produces `13` from a counter property, or `In Progress` from a name property. |
| `node_type` | The node's own `TypeKey`. Present only when an operator wants each kind numbered and addressed separately. |
| `literal` | Fixed text used as a prefix or separator. |

`scope_ref` names its domain by feature, not by type key, matching how
`PropertyDef.AppliesToFeatures` already scopes a property definition. The seeded
value is `FeatureIsScope`.

The four current strategies express as templates:

| Strategy | Template |
| --- | --- |
| `scoped_sequence` | `scope_ref(is_scope)`, `literal("-")`, `property(sequence)` |
| `scoped_property` | `scope_ref(is_scope)`, `literal("::")`, `property(name)` |
| `direct_property` | `property(identifier)` |
| `uuid_only` | Empty. No reference, no constraint. |

### The counter derivation rule

A value generator derives its key from the template, minus the part it fills.

For a counter filling `property(sequence)` in the template
`scope_ref(is_scope), literal("-"), property(sequence)`, the counter key is the
resolved scope node. `nodeType` is absent from the template, so `nodeType` is
absent from the counter key. Adding `node_type` to the template adds it to the
counter key.

This rule is what makes the operator's declaration authoritative over both the
numbering and the lookup. Whatever appears in the template appears in the
counter and in the rendered reference, without exception.

Deriving the counter key from the template changes its encoding, and an unseen
counter key starts at zero. The repair pass therefore seeds each counter key to
the highest value it observes in that scope before any new node is created
against it. Without that step the counter reissues values already in use.

### Enforcement

Two generic key families carry the constraint:

```
(node_reference,       orgID, templateName, encodedKey) -> nodeID
(node_reference_owned, orgID, nodeID, templateName)     -> encodedKey
```

`encodedKey` is the tuple-packed rendered parts. The forward family enforces
uniqueness and answers lookups. The reverse family lets a node's prior keys be
found from its identifier alone, which `Delete` and `UpdateAtomic` need because
neither can render a key without NodeType metadata. `relationship_reverse` exists
for the same reason, and `Delete` currently carries a comment noting the absence
of a per-node reverse index for property values.

Both families are generic over NodeType metadata and privilege no concrete type.

Four write paths carry reference keys. `CreateAtomic` inlines its own primary,
resolve, and view writes. `Set` and `UpdateAtomic` share `writeNodeRecords`.
`Delete` clears the node's records. Each writes or clears reference keys inside
the transaction it already opens, so a node's primary record, view, resolve
record, property indexes, and reference keys commit or fail together.

On write the transaction reads the forward key first. A key already held by a
different node fails the transaction with a conflict error naming both nodes. A
key held by the same node is a no-op.

On update the reverse range yields the node's prior keys. Those forward keys are
cleared and the new ones written, mirroring `reconcilePropertyIndexes`. This
closes the second duplicate mechanism: moving a node into a scope where its
rendered reference is taken fails instead of silently colliding.

The storage layer never reads PropertyDefs or NodeTypes, per the existing
contract on `NodeRepository`. The service renders the keys and passes them in,
exactly as it already passes `indexedProps`.

### Resolution

`resolveSequenceNodeID` becomes a point read on the reference key. Parsing an
incoming reference walks the same template that renders it, so the parse and the
render cannot disagree.

Resolution stops scanning `node_by_property`, stops walking the parent chain in
Go for each candidate, and stops looping over every sequence-bearing type per
workspace.

For a node predating the reference key, the point read misses. The resolver then
falls back to the existing scan path and routes the result through `uniqueMatch`,
so an unrepaired duplicate raises `matched multiple nodes` with both UUIDs rather
than resolving to one. The fallback is removed after the repair operation reports
zero duplicates across every org.

### What the seed writes

No template is built into the code. A NodeType with no template carries no
constraint and gets no rendered reference. There is no fallback template, no
implicit part, and no code path that supplies one.

The seed writes a starting suggestion the operator owns and may edit or delete.
It gives `issue`, `epic`, `cycle`, and `module` the template
`scope_ref(is_scope), literal("-"), property(sequence)`, omitting `node_type`.
`FAN-13` then names exactly one node across every work-item type, matching what
the rendered string already implies to a reader.

That template makes the existing epic and issue sharing `FAN-1` invalid, along
with `FAN-2` and `FAN-5`. The repair operation reassigns one node in each pair.

The TACK project carries the same collision at scale. Its epic counter reaches
26 while its issue counter passes 369, so every epic reference from `TACK-1` to
`TACK-26` names an issue as well. The repair reassigns the epics, because the
issues are older and the retention policy keeps the older node. Verified
2026-08-05: `tack_get_epic` and `tack_get_issue` each resolve `TACK-26`
correctly because typed resolution passes only its own type key, while
`tack_list_relationships` resolves it to the epic, so the untyped relationship
tools reach only one of the pair.

A deployment wanting independent numbering per type declares `node_type` in its
own template instead. Both nodes then keep separate counters and render
distinguishable references. The engine enforces either declaration identically;
neither shape is the engine's opinion.

## Repair

### Detect

A maintenance operation registered through `ops.Register` reports, for every org
and every NodeType carrying a template, each rendered reference held by more than
one node, with every candidate UUID and creation time.

Detection is a read-class operation. Under the ops audit contract in
`docs/operator-identity-and-audit.md`, a read records one access event before it
runs and fails closed if that event cannot be written.

The first run answers whether projects beyond FAN carry duplicates. That scope is
currently unknown; the eight confirmed duplicates are all in FAN.

### Renumber

Renumbering changes a rendered reference that a person may have written down.
The operation prints the complete old-to-new mapping and writes it into the audit
ledger before applying anything.

Grouping is by rendered reference. Every node in a group except the retained one
draws a fresh value from the counter, which sits at the high-water mark for that
scope.

Which node is retained is a policy of this operation, not a rule in the storage
layer. The enforcement path rejects a duplicate and holds no opinion about which
node should win. The operation takes a `--keep` flag with values `oldest` and
`newest`, defaulting to `oldest`. `oldest` orders by UUIDv7, which sorts by
creation time and needs no additional stored data, and leaves a stale reference
pointing at the older node, which is the more probable referent of older text.

The same pass writes the reference key for every node it visits, so one run both
repairs duplicates and backfills enforcement data for nodes created before the
template existed.

Renumbering is an FDB mutation. Under the ops audit contract it declares an
audit specification with `Atomic` set, so its ledger event commits in the same
transaction as the change and the two cannot disagree.

### No forwarding record

A forwarding record maps one prior reference to one successor node. A duplicated
reference had two referents, so a forward from it has two candidate targets and
no rule for choosing between them. Building one would restore the ambiguity this
design removes.

Forwarding is well defined for a different operation, moving a single node
between scopes, where the prior reference had one meaning and one successor.
That operation is out of scope here and is tracked as an optional feature request
on TACK-365 (`Optional reference forwarding for nodes that move between scopes`,
low, Todo).

Once uniqueness is enforced at write time, a rename caused by a duplicate can
occur only during this one-time repair. A one-time event does not justify a
permanent structure in the data model.

The audit ledger entry is the recovery path for a stale reference. A stale
reference resolves to the node that kept it, which is the older node, and
therefore the more probable referent of older text.

No internal record addresses a node by its rendered reference, so relationships,
comments, activity, workflow-state pointers, and audit rows are unaffected by a
rename.

## Testing

Reproduce through real code paths so the tests prove the defect rather than
restate it.

**Cross-type reproduction.** Create a project, then an epic, then an issue.
Both receive sequence 1 and render the same reference. Two ordinary create calls.

**Same-type reproduction.** Create issues under two separate scopes, then run
`repair.sequence_scope_ids`, which rewrites `scope_id` and leaves `sequence`
untouched. A node arrives in a scope whose counter already issued its number.

**Tooling.** Run detect and assert both duplicate groups appear with their
UUIDs. Run renumber with the execute gate and assert the printed mapping matches
the ledger entry.

**Post-repair assertions.** Every rendered reference resolves to exactly one
node. A fresh create in the repaired project receives a value above the previous
high-water mark. Repeating the cross-type reproduction fails the create with an
invalid-argument error rather than producing a duplicate.

**Template-driven assertions.** A NodeType whose template declares
`property(number)` allocates into `number`, renders from `number`, and resolves
by `number`, with no code path reading the literal `sequence`. A NodeType whose
template includes `node_type` permits an epic and an issue at the same numeric
value and renders them distinguishably.

Unit tests cover template rendering, template parsing, and counter-key derivation
with no database. Integration tests run through `make test-integration`, which
brings up single-node FoundationDB and YugabyteDB in containers.

## Rollout

1. Land the template declaration, the reference key family, and the enforcement
   in the shared write path, with the scan fallback in place. Existing behavior
   is unchanged for non-duplicate references.
2. Land detect. Run it against QA and against production read-only to establish
   the true blast radius across every project.
3. Land renumber. Validate on QA, which is disposable and recreated to mirror
   production, per the QA-first requirement in `AGENTS.md`.
4. Run renumber against production with the execute gate. Record the mapping in
   the ticket.
5. Seed the default templates for the built-in work-item types and run renumber
   again to resolve the cross-type collisions the new default creates.
6. Remove the scan fallback once detect reports zero duplicates across every org.

## Work breakdown

One epic with the following tasks.

1. Reference key template types on `NodeType`, with render, parse, and
   counter-key derivation. Unit tested, no storage change.
2. The `node_reference` key family and enforcement inside `writeNodeRecords`,
   including clearing the prior key on update.
3. Point-read resolution with the scan fallback routed through `uniqueMatch`.
4. Replace the literal `"sequence"` in `allocateCreateSequence` and
   `resolveSequenceNodeID` with the property the template names.
5. Detect operation, read-class, registered through `ops.Register`.
6. Audit every project in every org for duplicate references, using detect.
   Record the results on TACK-342.
7. Renumber operation, atomic mutation class, with the `--keep` flag.
8. Seeded default templates for `issue`, `epic`, `cycle`, and `module`.
9. Integration regression covering both reproductions and the post-repair
   assertions.
10. Remove the scan fallback.

TACK-342 is the existing ticket for the defect. Its description records four
duplicate references; the verified count in FAN is eight.

## Open questions

None blocking. Forwarding is settled as no forwarding record, for the reason
given in the repair section.
