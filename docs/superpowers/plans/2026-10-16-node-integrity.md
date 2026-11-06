# Node integrity and audit implementation plan

Implement the [node integrity specification](../specs/2026-10-16-node-integrity.md) through the existing generic node and audit boundaries. Concrete production repairs retain the existing manifest review and execution requirements.

The plan uses Go, real FoundationDB, YugabyteDB, Kafka, the existing repair console, and the public MCP interface. The [search correction plan](2026-10-16-search-correction.md) covers search implementation, complete acceptance, and coordinated delivery.

Use the [parallel implementation plan](2026-10-17-parallel-implementation.md) for source ownership, transaction contracts, repair checkpoints, and concurrent execution.

## Establish the implementation base

1. Recheck current main, deployed images, ticket states, active worktrees, and the Tack and Proxmox deployment owners. Use an isolated worktree and preserve ongoing implementation branches.
2. Read the current [repository contract](../../../AGENTS.md), [operator audit rules](../../operator-identity-and-audit.md), and [Makefile](../../../Makefile). Run `git status --short`, `git rev-parse HEAD`, and `make build` as separate commands before code changes. Record baseline failures explicitly; do not accept new baseline findings.
3. Apply the specification and record mappings. Report a new mismatch when current source evidence invalidates a mapping, and continue independent tasks.
4. For each coding task, add the smallest necessary regression through a public boundary with real dependencies. Extend QA data generation for changed runtime paths. Run `make build`, the relevant real regression, and `git diff --check` before committing that task.

## 1. Establish one generic metadata contract

TACK-579, TACK-580, and TACK-584 share this prerequisite. TACK-583 can proceed independently, but its transactional interface must agree with tasks 2 and 3.

Modify [node metadata](../../../internal/domain/node/types.go), [property validation](../../../internal/service/node_props.go), [property command compilation](../../../internal/service/property_command.go), and the [generic hierarchy interpreter](../../../internal/domain/node/hierarchy.go).

1. Map capabilities, required fields, defaults, enum options, reference constraints, and containment semantics from stored metadata. Separate product definitions from generic interpretation.
2. Resolve the `scope_id` applicability disagreement through a controlled correction of the fixed definitions and service contract. Preserve legitimate comment, activity, state, and project values. Do not replace the current exemption with another concrete property-name list.
3. Define the canonical parent and scope interfaces once. A same-project containment move must preserve numbering scope and reference identity. Any supported scope-changing operation must update references, indexes, access, and search work atomically according to its existing contract.
4. Admit valid nested work items through the generic contract. Remove reader and search access dependence on enumerated product type pairs without weakening organization or permission checks.
5. Trace old seed, import, backfill, and internal writes for the identified violations. Attach exact supporting records or mark the historical cause unknown.

Verify through public create/read/update operations with unfamiliar type and property identifiers, supported nesting, and legitimate scope stamping. Use the existing metadata fixture facilities without exposing a new production schema editor. Run `make test-integration` and confirm the affected scenarios execute rather than skip.

## 2. Validate the resulting node before persistence

TACK-580 implements N1 and N2 after task 1 defines applicability. Modify [create defaults](../../../internal/service/node_defaults.go), [create preparation](../../../internal/service/node_create_prepare.go), and [node update](../../../internal/service/node.go). Reuse the property compilation and validation files from task 1.

Separate the shared validator's use by repairs from its activation for ordinary writes. Deploy the repair prerequisites while existing public write checks remain active. Enable strict validation for ordinary writes only after task 5 repairs and verifies the old data. Use release sequencing rather than temporary exceptions for invalid records.

1. Apply supplied fields and removals to the committed node, then validate the resulting record. Create applies declared defaults before the same validation. Preserve unspecified fields during updates.
2. Check declared names, applicability, primitive JSON shapes, select and multiselect option keys, reference constraints, required fields, and permitted removal. Resolve labels through generic metadata only when the existing input contract permits labels; otherwise reject them clearly.
3. Apply this contract to ordinary tools, workflow-reference updates, operator mutations, imports, and repairs. Audit storage entry points for bypasses and enforce the invariant at the shared mutation boundary.
4. Reject a mutation before any primary, view, resolution, relationship, index, search-work, or success-audit write becomes visible. Account for metadata and source revisions that change during validation.

Create a public-boundary regression in `internal/test/integration/node_property_validation_test.go`. Exercise unknown fields, `High` versus key `high`, wrong value shapes, invalid references, clearing a required field, valid defaults, and valid partial updates. Read the complete stored result after each rejected operation and compare it with its starting state. Use real FoundationDB; do not replace persistence with a test double.

Run `make test-integration` and inspect the named regressions. Expect invalid requests to fail clearly and valid updates to preserve unrelated fields.

## 3. Make parent mutations and deletion consistent

TACK-584 implements N4 after task 1. Modify [parent command resolution](../../../internal/service/property_command_parent.go), [parent update preparation](../../../internal/service/node_update_parent.go), [atomic updates](../../../internal/adapters/foundationdb/node_update.go), [parent-edge removal](../../../internal/adapters/foundationdb/relationship_parent_guard.go), and [deletion traversal](../../../internal/adapters/foundationdb/node_delete_step.go). Review [deletion moves](../../../internal/adapters/foundationdb/node_delete_move.go) with that traversal.

1. Resolve UUID and printed parents under the same generic constraints. Check the target and ancestor chain for self-parenting, cycles, organization, and scope consistency.
2. Read and validate the current source revision and parent inside the transaction. Recompute the relationship mutation or reject stale preparation; do not apply edge changes prepared against an obsolete parent.
3. Include the relevant ancestry and competing deletion reads in transaction conflict detection. Require one matching canonical edge after concurrent successful moves.
4. Protect sole-parent removal using the canonical parent property even when older metadata does not recognize a stored pair.
5. Detect intended children from the canonical record contract, including records with a missing edge. Perform the declared cascade or move behavior, or refuse the deletion before changing records. Preserve other relationship types.

Create `internal/test/integration/node_parent_integrity_test.go` and extend [public delete coverage](../../../internal/test/integration/mcp_delete_moves_children_test.go). Test UUID/printed parity, nested nodes, self and deeper cycles, simultaneous moves, a move competing with deletion, sole-edge removal, and disagreement between old properties and edges. Use concurrent public calls against real FoundationDB and inspect final primary records, read views, and relationships. Assert outcomes and invariants rather than a particular scheduling order.

Run `make test-integration`. Expect either a valid complete mutation or a clear rejection with unchanged records. Repeat the concurrent cases to establish conflict handling.

## 4. Record actual committed audit deltas

TACK-583 implements N7. Modify [event staging](../../../internal/audit/intent.go), the node mutation boundary from tasks 2 and 3, and [metadata repair event staging](../../../internal/ops/search_projection_audit.go). Inspect [intent persistence](../../../internal/adapters/foundationdb/ops_outbox_intent.go) before choosing the exact transaction interface.

1. Compute changed field identifiers and before-and-after values from the transaction's actual source and resulting records. Include removals, normalized references, parent changes, and fixed-metadata corrections.
2. Commit that event intent with the mutation. Preserve event identity and actor context across delivery retries; a transaction retry must not publish an abandoned value.
3. Preserve the existing relay, Kafka, single ledger writer, and chain append behavior. Use the native audit reader to verify the stored event. Do not rewrite old audit rows.
4. Inspect older audit design text for direct-writer instructions. Replace only instructions that conflict with the current atomic-outbox and single-consumer contract.

Extend [real audit intent coverage](../../../internal/test/integration/audit_intent_test.go). Exercise a public generic edit, workflow reference, removal, parent change, rejected request, retry, and repair. Verify committed values through serialization, intent, relay, Kafka, ledger persistence, and native audit query using real dependencies. The rejected request must not produce an applied delta.

Run `make test-integration`. Expect each successful mutation's delta to match the stored result, with intact actor and chain verification. Preserve a QA ordinary-update receipt for the production comparison.

## 5. Implement the reviewed existing-data backfill

TACK-582 implements N6 using the [selected backfill mappings](../specs/2026-10-16-node-backfill.md). The shared validator and the metadata, parent, and audit corrections establish its target contract. Strict validation for ordinary writes activates after verified cleanup, not before this backfill. Regenerate the manifest from current records; do not execute the older captured proposals directly.

Reuse [manifest preview and apply](../../../internal/ops/command_repair_manifest.go), [repair profiles](../../../internal/ops/repair_profile_types.go), [repair transforms](../../../internal/ops/repair_props_transform.go), and [repair execution](../../../internal/ops/repair_console.go). Add the temporary command in `internal/ops/cli_node_integrity_backfill.go` and its bounded implementation in `internal/ops/node_integrity_backfill.go`. Split by concern before exceeding the repository file limit.

1. Reconcile the inventory with TACK-206 tooling and the complete scopes of TACK-220 and TACK-221. Add only missing generic capabilities; retain those tickets until full supersession is proved.
2. Require a complete manifest with source hashes/revisions, exact before-and-after records, evidence, decisions, unresolved entries, and stable repair identities. Validate every entry before admitting writes. Unknown, duplicate, conflicting, and unreviewed actions must fail clearly or remain explicit nonexecuting entries.
3. Register the proposed `ops backfill once-node-integrity` command through the existing `clispec` mechanism. Declare TACK-582 and a literal removal date of December 31, 2026. Confirm its generated command and parameters through help before use. Keep the existing operator identity, audit, preview confirmation, and execution gates.
4. Implement bounded progress and transaction checks. A source mismatch invalidates the old preview. Use durable checkpoints for a report-comment create, readback verification, and obsolete-property removal. A retry must reuse the same comment.
5. Combine overlapping approved patches per node and validate the complete result. Defer a node when an unresolved field prevents that validation. Update nodes, views, resolution records, indexes, matching edges, audit intent, and desired search work through the shared atomic contract. Preserve unrelated data and available provenance.
6. Report planned, applied, unchanged, conflicted, unresolved, and failed entries separately. Reread changed records and verify the resulting graph. Require an unchanged second preview for completed entries. Complete a broad validation scan before enabling strict ordinary-write checks. Prove that an intervening invalid write invalidates the scan-to-activation check; do not treat an earlier clean scan as proof of a later state.

Create `internal/test/integration/node_integrity_backfill_test.go`. Enter through the actual command boundary against real FoundationDB and audit dependencies. Cover every repair class, nonmutating dry run, a changed source, partial failure, interrupted report migration, property-history precedence, the approved latest-explicit-dated-decision fallback, absent or conflicting decision evidence, graph ambiguity, and a repeated execution. For the selected test-comment cleanup, require a verified complete export before deleting the exact UUID; prove that export failure and stale source prevent deletion and a repeated execution preserves other records. For duplicate-notice reattachment, verify the new parent and matching representations while preserving the original ID, text, creator, creation time, and unrelated relationships; require a repeated execution to avoid replacement or duplicate comments. Seed synthetic test records through the existing QA fixture facilities; do not copy private production exports into the repository.

Run `make test-integration` and the command's help and preview against the test fixture. Expect complete per-record output and preserved unresolved records. QA execution requires the reviewed mappings. Production execution still requires approval of its concrete repair manifest.

## 6. Complete delivery without losing scope

Use the search correction plan for release order and the full acceptance evidence. Keep prevention, source-data repair, and search recovery as distinct results. Independent audit and validation development can continue during repair preparation and verification.

Before closing a ticket, attach the canonical requirement and plan links, relevant merged commits, installed revisions, and direct acceptance evidence. Close a superseded ticket only after the replacement covers its full scope.

Completion requires the reviewed schema and relationship contract, proven prevention, accurate future deltas, an accounted-for backfill result for every class, and full search and delivery evidence. Unapproved records remain visible as unresolved work.
