# Search projection and recovery implementation details

Implement the search outcomes from the [parallel plan](2026-10-17-parallel-implementation.md). Preserve the [search acceptance contract](../specs/2026-09-19-search-acceptance.md), except its explicitly changed inclusion and invalid-content rules.

## Reuse existing changes

The open parent work already adds `node.HierarchyParent`, removes type-pair filtering from access traversal, and adds real parent-edge tests. Assign the remaining access integration to Mutation.

The open property-check work adds node IDs and sorted unknown-property names to fatal projection errors. Retain those identities while replacing whole-node rejection expectations with valid-content indexing and warnings.

## Return content and property diagnostics separately

Projection owns [projection declarations](../../../internal/domain/node/search_projection.go), [rendering](../../../internal/domain/node/search_projection_emit.go), [content results](../../../internal/domain/node/content.go), [content reads](../../../internal/adapters/foundationdb/node_content.go), [snapshots](../../../internal/adapters/foundationdb/node_content_snapshot.go), and [readiness](../../../internal/ops/search_projection_gate.go). Coordinate [definition storage](../../../internal/adapters/foundationdb/property.go) with Schema and Migration.

1. Consume Schema's applicability and validated-value interpretation. Read node type metadata in the source snapshot; definitions for present property names alone do not establish applicability.
2. Preserve explicit `Search.Include=false`. Derive absent optional metadata from the generic schema representation. Reuse scalar, object, item, and label conversion without product-name branches.
3. Render deterministic complete Unicode text. Index valid declared content despite another invalid or unknown property. Preserve invalid source values and omit them from page text.
4. Return diagnostics with content. Reserve error results for operational or undecidable required-metadata failures. A failed source read must not become a property warning.
5. Accept absent optional inclusion metadata in readiness and definition storage. Distinguish it from malformed required schema metadata.

Proposed `PropertyDiagnostic` fields include organization, node, property name, optional definition ID, source revision, projection version, stable reason code, and readable reason. Do not copy invalid source values. Expose bounded diagnostic pages or a revision-bound diagnostic cursor; do not add dynamic OpenSearch fields.

## Persist only current warning state

Projection owns warning records and listing. Integrate checkpoints through [work registration](../../../internal/adapters/foundationdb/search_work_register.go), [work progress](../../../internal/adapters/foundationdb/search_work_progress.go), and shared ports under Integration. Extend [verification output](../../../internal/ops/search_verify_exclusions.go) without conflating warnings and exclusions.

1. Derive warnings from the text's source snapshot, generation, and projection.
2. Stage the set at content registration. Promote it after all pages finish and refresh succeeds.
3. Replace the old verified set after convergence. Clear a repaired warning after its repaired revision finishes indexing.
4. Preserve verified warnings during failed reads, engine errors, partial pages, and obsolete claims. Reject delayed attempts to restore an older set.
5. Clear warnings with completed node deletion. Store current state rather than history, with bounded cursor reads and output.

Use typed `PropertyWarning`, `AuthorizationExclusion`, and `OperationalFailure` outcomes. A property warning does not fail an otherwise valid indexing slice. An unresolved permission boundary still withholds the node.

## Preserve work after operational failures

Recovery owns [worker settlement](../../../internal/service/search_worker_budget.go) and [failure persistence](../../../internal/adapters/foundationdb/search_exclusion.go). Reconcile the settlement edit already present in the parent PR with its owner.

At this baseline, `settle` counts failed source reads, and `countFailure` excludes live/access/copy nodes after five counted attempts. That clears desired work for an operational failure.

1. Preserve pending work after source reads, engine requests, and model readiness fail. Use existing bounded retries and backoff.
2. Preserve attempt counts and stuck-work reporting. Crossing a reporting threshold must not exclude the node or clear desired work.
3. Preserve noncounted engine-outage behavior where the acceptance contract requires it. Report work identity, generation, context, and last error.
4. Reserve node exclusion for explicit authorization outcomes. A permission repair schedules the existing readmission work.

Prove repeated real failures beyond the reporting threshold, retained work and explicit verification output, then automatic convergence after recovery without another source edit.

## Version all rendering inputs

Migration owns [projection hashes](../../../internal/adapters/foundationdb/node_content_projection.go), [metadata updates](../../../internal/adapters/foundationdb/search_metadata.go), [rescan requests](../../../internal/adapters/foundationdb/search_rescan_request.go), and [reader cursors](../../../internal/adapters/foundationdb/node_content_cursor.go).

1. Include renderer semantics and all default-rendering dependencies in projection identity: property type, options, applicability, reference representation, and relevant node features. Preserve the separate permission-policy contract.
2. Add a versioned digest namespace. Recompute bounded contributions and verify the metadata epoch before activation. Do not subtract a new-format hash from an old-format additive digest.
3. Add explicit full-content rescan input. An empty name list currently returns before setting `Content`; `requestSearchRescan(..., nil, false)` does not request content work.
4. Define a typed request for named-property content, all content, and access work. All-content requests dominate older named requests, preserve progress, and revisit earlier records after a newer request.
5. Commit corrected definitions, matching digest/version, audit intent, and desired work together. Reuse the normal metadata update path; `SetProjectionIfNil` omits digest changes and rescans.
6. Activate a durable renderer version after all serving writers support it. Bind cursors and registration to it. Reject obsolete cursors and restart pending work through the normal rescan path.
7. Check the active organization projection at registration and refresh. Comparing only saved progress does not reject every delayed older writer.
8. Schedule affected nodes even when values and definitions remain unchanged. Use bounded work on the serving physical index and retire obsolete affected documents. Preserve explicit exclusions and unaffected pages.

A renderer constant alone does not schedule work or reject old writes. Verify compatible writers before activation; a binary that ignores the activation record must not remain a writer.

Test unchanged definitions during an interpreter upgrade, interrupted digest recomputation, concurrent metadata writes, overlapping full/named rescans, obsolete cursors, delayed writers, and repeated migration. Require convergence without another source edit or physical index replacement for a text-only correction.

## Preserve access through continuation and replay

Mutation integrates [access traversal](../../../internal/searchaccess/hierarchy_walk.go), [dependent enumeration](../../../internal/searchaccess/hierarchy.go), and [summary authorization](../../../internal/adapters/foundationdb/node_summary_hierarchy.go).

1. Use one canonical containment interpretation for indexing and final batched checks. Do not infer parents from project scope or repair source data during reads.
2. Preserve organization checks, missing-ancestor errors, cycles, current membership, opaque filters, and denial when the permission boundary remains unresolved.
3. Schedule bounded access refresh or an existing policy transition after interpretation changes. A code merge does not update indexed keys.
4. Prove current authorization after parent moves, deletion, and revoked membership during continuation and replay.
5. Prove access-only updates without inference and without changing document IDs, text, chunks, or sparse weights.

Reuse the parent-edge test family, including nested leaves and moved chains. Do not create another generic-access implementation.

## Diagnose the production relevance regression

Relevance owns [query construction](../../../internal/adapters/search/opensearch_query_body.go), [sparse tokens](../../../internal/adapters/search/opensearch_tokens.go), [session inference](../../../internal/service/search_query.go), and [ranking tests](../../../internal/test/integration/search_ranking_test.go). Coordinate rendering changes with Projection if traces identify that cause.

The query combines lexical `name^3` and `page_text` with the greatest nested sparse score. Access, retirement, and optional type filter before ranking. Session creation stores query weights once; continuation reuses them.

1. Add `restart containers one by one`, TACK-578's content shape, and plausible distractors as an additional public regression.
2. Reproduce the miss on QA with recorded model, tokenizer, mapping, images, projection, and corpus. Capture indexed text, native chunks/weights, saved query weights, exact filters, score contributions, and node ranks.
3. Correct the observed cause. Do not add phrase branches, maintained synonyms, another tokenizer, or weaker rank bounds.
4. Preserve 150 distractors and 12 target variants for the six existing queries. Repeat each three times before and after replacement. Retain lexical control and one-inference continuation/replay proof.

## Reconcile fixtures and complete acceptance

Integration coordinates fixture changes with each owner. Some projection fixtures use random structural property types. `putOpaqueNode` omits `parent_id` while adding an edge, and `moveOpaqueNode` edits edges directly. Use valid structural types and the checked mutation contract while retaining unfamiliar product identifiers. Use explicit legacy-corruption fixtures only for targeted defect scenarios.

Update only the conflicting inclusion and invalid-property clauses in the existing search documents. Preserve every other acceptance section.

Run focused public tests, then `make build`, `make test-integration`, and full search groups on the combined revision. G4/G6 require `TACK_SEARCH_CLUSTER=1`; run applicable T1/T2/T3 repeated groups. Reserve heavy engine resources and QA windows with Integration.

Retain public search-only and simultaneous workloads, marker freshness, backlog age, latency, resources, restore/recovery, and production capacity evidence. OS-process throughput and ordinary soak do not establish search capacity.

The reader currently renders a complete node before slicing pages. Small pages do not prove bounded memory for a larger source. Preserve the existing storage-expansion condition for larger-than-worker-memory acceptance; do not infer that result from small-node tests or add a new storage feature to this correction.
