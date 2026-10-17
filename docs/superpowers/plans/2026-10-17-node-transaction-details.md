# Checked node mutation and repair implementation details

Implement the Mutation, Schema, Audit, and Repair outcomes from the [parallel plan](2026-10-17-parallel-implementation.md). Preserve one canonical write contract and the [selected backfill mappings](../specs/2026-10-16-node-backfill.md).

## Define required mutation inputs

At baseline `264759cc`, the service prepares a replacement node and relationships before `UpdateAtomic`. That transaction does not reread its source. `UpdatedAt` is not a full mutation revision, and a search content revision is not a general source revision.

Mutation replaces prepared-write arguments with a required typed `NodeMutationPlan`. These are proposed additions to the existing node domain module:

| Contract | Required contents and behavior |
| --- | --- |
| `MutationSnapshot` | Include node identity, authoritative primary encoding/digest, and relationship snapshots needed for preparation. Obtain them through the node-reader boundary in one FoundationDB transaction. |
| `SchemaSnapshot` | Include the type index, applicable definitions, and existing metadata epoch. Supply one generic applicability and value interpreter. |
| `NodeMutationPlan` | Include identity, partial name/property changes, a typed expected-existing digest or expected-absence condition, metadata epoch, reference/graph expectations, and noncontainment relationship changes. Include an internal checkpoint mutation only for an audited repair. |
| `MutationEventBuilder` | Consume immutable event identity/context and each attempt's actual source/result snapshots. Return opaque event JSON without external side effects. |
| `MutationReceipt` | Return the committed identity, source/result digests, and completed repair phase from the successful transaction attempt. |

Use the complete authoritative primary encoding for the initial source digest. Canonicalize graph inputs deterministically and identify every record that influenced target selection or reference resolution. Do not substitute timestamps or search generations for these preconditions.

Define mutation expectations only in service and adapter APIs. Public callers must not receive an option that disables validation or selects an unchecked writer.

## Reconstruct mutations inside the transaction

Mutation owns [service updates](../../../internal/service/node.go), [repository interfaces](../../../internal/domain/node/repository.go), [atomic persistence](../../../internal/adapters/foundationdb/node_update.go), and [transaction retries](../../../internal/adapters/foundationdb/node_transaction.go).

Mutation also owns [service creation](../../../internal/service/node_create.go) and [atomic creation](../../../internal/adapters/foundationdb/node_create.go). Creation requires expected absence and checks the idempotency identity inside the transaction. A retry may return an existing receipt only when its source, request, and completed result match. Creation applies the same parent, deletion-state, metadata, audit, checkpoint, and activation checks as updates. Report-comment creation uses this boundary.

1. Read current resolution and primary records through ordinary transaction reads. Reject an expected-source mismatch with `domain.ErrConflict` and leave stored representations unchanged.
2. Read the metadata epoch and required definitions. Check the epoch used during preparation. Adapt the initial projection correction path to participate in that epoch contract.
3. Apply the patch to the transaction source. Omitted fields remain unchanged; `null` removes a property.
4. Read constrained references and the proposed parent/ancestors. Check existence, organization, declared constraints, project scope, cycles, and competing deletion state.
5. Derive containment changes from the actual source and resulting parent. Do not trust earlier prepared `child_of` changes. Preserve unrelated relationships.
6. Validate the result under the deployment stage's contract. Repairs invoke full schema validation before persistence.
7. Derive views, resolution, property indexes, reference claims, and audit deltas from that source/result pair.
8. Write records, relationships, desired search work, audit intent, and any repair checkpoint in the same transaction.
9. Return the successful attempt's receipt and mark its staged intent committed after persistence succeeds.

Every retry repeats the reads and creates fresh attempt-local output. Do not reuse abandoned edges, views, reference claims, deltas, or checkpoint results.

Audit `Set`, direct deletion, imports, metadata corrections, and repairs for unchecked alternatives. Remove those alternatives or apply the same invariants. Legacy-corruption tests may use test-only storage setup; production APIs must not expose that setup as a bypass.

## Interpret the fixed schema once

Schema owns [property definitions](../../../internal/domain/node/types.go), [property checks](../../../internal/service/node_props.go), [command compilation](../../../internal/service/property_command.go), [defaults](../../../internal/service/node_defaults.go), and [create preparation](../../../internal/service/node_create_prepare.go).

1. Add one pure applicability operation. Empty `AppliesToFeatures` applies universally; capabilities select other applicable definitions. Remove name-based exemptions only after fixed metadata represents legitimate existing values.
2. Add one pure result validator for required presence, JSON shapes, enum/multiselect keys, dates, UUIDs, and reference constraints. Consume transaction-read reference records rather than performing external reads from the interpreter.
3. Apply literal/reference defaults before create validation. Preserve label resolution only where the existing input boundary supports it. Do not infer general case folding from the three identified repair values.
4. Supply the same interpreted property result to search. Do not add another search-specific applicability or primitive validator.
5. Correct `scope_id` applicability without deleting legitimate values or building a public schema editor.

Expose proposed `PropertyAppliesTo`, `ValidateResult`, and a property interpretation result through the domain contract. Distinguish valid values, invalid values with property reasons, and failed source/metadata reads.

## Preserve canonical parents during deletion

Extend the existing parent PR. Mutation owns [parent preparation](../../../internal/service/node_update_parent.go), [edge removal](../../../internal/adapters/foundationdb/relationship_parent_guard.go), [deletion jobs](../../../internal/adapters/foundationdb/node_delete_job.go), [deletion steps](../../../internal/adapters/foundationdb/node_delete_step.go), and [deletion moves](../../../internal/adapters/foundationdb/node_delete_move.go).

1. Give UUID and printed references identical generic containment checks. Preserve project scope, sequence, and reference identity during a same-project move.
2. Protect the canonical edge even when older type-pair metadata rejects it. A redundant ancestor edge must not authorize removing the canonical edge.
3. Add transaction-readable deletion state. Creates and moves reject deleting parents; deletion steps reread children and destinations. An obsolete edge must not delete a child that a newer transaction moved.
4. Rebuild and verify canonical-parent discovery data during the backfill. Reverse edges and historical property indexes do not establish completeness after unchecked writes. Persist a completeness certificate bound to the verified source versions, metadata epoch, and index generation. Use the scan fence below to prove the scan stayed current.
5. Require a current certificate before deletion. Refuse deletion when the certificate is absent or stale. Every later writer maintains the canonical-parent index atomically with its primary change and preserves or invalidates the certificate under that protocol. Compare index candidates with reverse relationships and actual primary records. Perform the declared lifecycle or refuse before an unresolved disagreement creates an orphan.

Use the closest-linked-container rule only in reviewed repairs. Runtime reads and permission checks must not repair or select among conflicting parents automatically.

## Finalize actual audit deltas

Audit owns [event templates](../../../internal/audit/intent.go), [opaque intent state](../../../internal/auditintent/intent.go), and [metadata repair events](../../../internal/ops/search_projection_audit.go). Mutation and Migration integrate the builder inside their transactions.

1. Preserve actor, source, request correlation, event identity, and provenance in an immutable template. Preserve the existing event and delta wire format.
2. Finalize `Before`, `After`, and `Changed` from actual attempt-local snapshots. A removed key appears in the before snapshot and does not appear in the after snapshot. Use one field-identifier convention for universal fields and properties.
3. Serialize opaque event JSON at the existing intent boundary. Preserve the `auditintent`/`audit` import separation; do not distribute event construction throughout storage code.
4. Reuse an event identity for retries of one logical mutation. Give different repair phases and later metadata corrections different identities. Do not reuse an initial-backfill identity for a distinct correction.
5. Preserve outbox delivery, relay, Kafka, the single consumer writer, and ledger hash chains. Do not rewrite historical events.

Add real relay fixture support. The current audited MCP harness clears Kafka configuration, and an outbox-only assertion does not prove ledger delivery. Reuse production relay adaptation through a reusable construction boundary rather than a recording double.

## Apply frozen repairs with durable checkpoints

Repair owns [planning/application](../../../internal/ops/repair_console.go), [manifests](../../../internal/ops/repair_manifest.go), and [confirmation tokens](../../../internal/ops/repair_report.go). The current token uses update time and planned changes; the application transaction does not verify a complete frozen source.

1. Add manifest version/digest, stable repair IDs, exact records, primary/graph/schema expectations, combined resulting patches, evidence references, and executable/unresolved status.
2. Validate the complete immutable manifest before admitting writes. Apply bounded entries with transaction-time checks. A pretransaction replan alone does not prevent concurrent changes.
3. Store dedicated repair checkpoints in FoundationDB. Proposed `RepairCheckpoint` fields include manifest/repair identity, source digest, phase, comment ID, verified content/export digest, and completed receipt. Do not reuse search-work state.
4. Atomically create a complete report comment and persist its identity. Read back its text and attachment. Recheck source and comment evidence when removing the old property, then complete that checkpoint. Reuse create idempotency with a repair source and durable progress.
5. Export the exact test comment's complete record and relationships. Read the protected artifact back and verify its digest against current source before committing deletion and its receipt. An absent node alone does not prove completed repair.
6. Reattach comments and repair parents without replacing identities or unrelated data. Recheck the simulated graph and original ancestor associations against current records.
7. Register through existing `clispec` identity, a real `DryRun`, execution gate, audit, and expiration. Use TACK-582 and the specified literal removal date. Do not set `audit.Spec.Atomic`; the current CLI specification rejects it.

Use the actual operator principal from context. Record any distinct on-behalf-of identity explicitly. A legacy `--actor` input does not replace operator identity.

## Activate strict validation after cleanup

Deploy mutation, metadata, audit, and repair prerequisites while ordinary writes retain existing checks. Repairs use complete-result validation. Activate that same validator universally after reviewed cleanup and a current broad scan.

Close the scan-to-activation race with an internal write fence. Every product mutation reads an activation record and atomically increments the epoch for its affected hash bucket. Use bounded buckets rather than one hot global write counter. Capture the bucket vector and metadata epoch before reading records. Bind scan checkpoints to that initial evidence. Require both values to remain unchanged after the scan and inside the activation transaction. Invalidate the scan and rescan affected records when either changes. Activation writes the record after those comparisons. That write conflicts with older in-flight mutation reads. Retried mutations then enforce the active strict contract.

Verify that every product writer supports the fence before activation. A writer that ignores it invalidates scan evidence. Retain strict validation after activation; do not expose caller-controlled bypasses or permissive per-node exceptions.

## Prove public behavior

Use real FoundationDB through public operations for invalid names/shapes/options, required removals, defaults, partial updates, reference parity, nested parents, cycles, concurrent moves, deletion races, and missing/disagreeing edges. Inspect final primary, view, resolution, index, relationship, desired work, and audit state.

Run actual CLI repairs for every mapping, nonmutating preview, stale primary/graph/metadata evidence, interrupted phases, export failure, exact deletion, preserved reattachment provenance, and unchanged reruns. Extend QA data generation for changed public paths.

Run focused worker selections, then `make build` and `make test-integration` on the combined revision. Audit proof includes real intent, relay, Kafka, ledger persistence, native `audit query`, actor identity, deduplication, and chain verification. An outbox assertion alone does not complete that proof.
