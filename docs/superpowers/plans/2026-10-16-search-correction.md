# Search correction and delivery implementation plan

Implement N3, N5, and N8 through N10 from the [node integrity specification](../specs/2026-10-16-node-integrity.md). Coordinate the shared interfaces with the [node and audit plan](2026-10-16-node-integrity.md). This draft retains unresolved choices and does not authorize source-data repairs.

## 1. Correct generic access and inclusion policy

TACK-579 and TACK-580 own the correction. Modify [search access traversal](../../../internal/searchaccess/hierarchy_walk.go), [hierarchy rules](../../../internal/searchaccess/hierarchy_rule.go), [property text conversion](../../../internal/domain/node/search_projection_emit.go), [projection metadata](../../../internal/domain/node/search_projection.go), and [readiness checking](../../../internal/ops/search_projection_gate.go).

1. Make access traversal use the canonical generic parent and permission contract. Preserve valid nested nodes and separate project scope. Keep inaccessible nodes excluded when the authoritative permission boundary cannot be established.
2. Define a deterministic default representation for readable schema-defined values through the existing generic interpreter. Honor explicit exclusions, stable ordering, complete decoded text, and existing structured/label representations. Do not infer inclusion from concrete names or `Indexed`.
3. Make absent optional inclusion metadata use that default. Index valid declared content when another property is invalid or undeclared, as selected by the user. Omit only the invalid property and preserve its source value. Report the node, property, source revision, and reason through bounded diagnostics alongside search verification. Keep these content warnings distinct from authorization exclusions and operational failures; retain the existing retry and error behavior for failed reads or engine requests.
4. Reconcile the Searchable content section of the [search architecture](../specs/2026-09-19-search-design.md) and the conflicting Opaque metadata clauses of [search acceptance](../specs/2026-09-19-search-acceptance.md). Preserve every unrelated criterion. Update conflicting comments and tests with that same contract.

Extend [property-decision tests](../../../internal/test/integration/property_search_decision_test.go), [projection readiness tests](../../../internal/test/integration/search_projection_readiness_test.go), and [permission identifier coverage](../../../internal/test/integration/search_permission_identifier_test.go). Use public boundaries with real dependencies, unfamiliar identifiers, schema-backed scalar text, explicit exclusions, nested nodes, and unchanged tenant checks. Seed an old invalid record through fixture facilities, then prove its valid text remains searchable, its invalid property remains unchanged and unindexed, and diagnostics identify that property. Repair it through the approved boundary and require content refresh and warning removal. Verify that revoked access still withholds the node and failed source reads still retain pending work. Reuse existing fixtures without adding a production schema editor.

Run `make build` and `make test-integration`; run the affected search group through `make test-search-group GROUP=G5`. Expect a missing optional inclusion declaration to preserve valid search behavior without exposing excluded content.

## 2. Correct existing metadata and schedule indexing

TACK-542 and TACK-580 own the migration. It depends on the selected policy and the audit correction for accurate repair deltas.

Inspect [initial projection writes](../../../internal/adapters/foundationdb/property_projection.go), [metadata updates](../../../internal/adapters/foundationdb/search_metadata.go), [projection versioning](../../../internal/adapters/foundationdb/node_content_projection.go), and [projection backfill](../../../internal/ops/search_projection_backfill.go).

1. Inventory explicit inclusion/exclusion decisions and preserve intentional exclusions. Preview each required fixed-definition correction and projection version change.
2. Reuse the existing transactional metadata-update path for corrections after activation. `SetProjectionIfNil` refuses changed existing declarations and omits rescan scheduling; do not use it as a general correction API.
3. Commit the corrected definition, projection digest/version, audit intent, and durable affected-content work together. Keep this an expiring controlled migration, not a new public schema feature.
4. Version interpreter semantics as well as stored declarations. The current version includes only the metadata digest and pagination version. A rendering change must invalidate old reader cursors and schedule every affected node even when its stored definition does not change. Resume old pending work under the correct new projection rather than mixing renderers within a cursor.
5. Process affected nodes through bounded work against the serving physical index. Preserve unaffected page documents; retire obsolete affected documents. Access-only corrections must preserve text and embeddings.
6. Verify interruption recovery, idempotence, metadata readiness, complete page coverage, and read/search consistency. Preserve the separate TACK-582 source-data manifest.

Extend [metadata refresh tests](../../../internal/test/integration/search_metadata_refresh_test.go) and [projection backfill tests](../../../internal/test/integration/search_projection_backfill_test.go). Include a reader cursor and pending indexing work created before an interpreter change while stored definitions remain unchanged. Run `make test-integration` and `make test-search-group GROUP=G5`. Expect explicit rejection of the obsolete cursor and complete convergence under the new projection without losing an explicit exclusion.

## 3. Diagnose and correct semantic retrieval

TACK-520 owns this task. Reproduce the specification's production failure on QA with the same content shape and recorded deployment configuration before choosing a fix.

Inspect [query construction](../../../internal/adapters/search/opensearch_query.go), [query inference](../../../internal/service/search_query.go), and [ranking fixtures](../../../internal/test/integration/search_ranking_corpus_test.go).

1. Preserve the failing query and expected TACK-578 content as an additional real public regression. Include plausible distractors and the production wording.
2. Capture actual indexed text, model/tokenizer identities, generated sparse fields, query inference output, structured filters, lexical boosts, and rank traces. Determine which operation causes the miss from those observations.
3. Correct the owning implementation without phrase-specific branches, maintained synonyms, a replacement tokenizer, or weaker acceptance bounds.
4. Repeat all accepted relevance cases three times and after reindexing. Include the lexical-only control and verify one inference per continued session. Explain any ticket-to-corpus count mismatch before claiming complete relevance coverage.

Run `make build` and `make test-search-group GROUP=G5`. A passing new regression does not complete task 4's full acceptance contract.

## 4. Verify every search acceptance section

TACK-544 owns the complete result. Maintain an evidence row per criterion with the exact revision, environment, command, input/corpus, expected result, observed result, and artifact. Mark an unrun criterion unverified. Do not substitute a green verification command for behavior acceptance.

| Accepted section | Evidence required for this correction |
| --- | --- |
| Evidence and environment | Exercise public operations with real FoundationDB, OpenSearch, local model, and authentication. Record image digests, configuration, model/tokenizer checksums, corpus, workload, bounds, and native client operations. |
| Build and lint gates | Prove the unedited base and every changed task through `make build`. Connect production declarations immediately and accept zero new lint findings. |
| Meilisearch removal and temporary search outage | Verify the completed removal contract and remaining deployment state. Preserve FoundationDB as the only rebuild source. Do not rerun the historical outage or delete a retained old volume merely to validate this correction. |
| Opaque metadata | Replace identifiers, omit product seeds, exercise missing optional inclusion defaults, explicit exclusions, deterministic rendering, name-only nodes, metadata refresh, valid-content indexing with separate invalid-property warnings, and fixed engine mappings. Use fixtures for future metadata-edit scenarios without building a public editor. |
| Semantic relevance | Run the complete accepted corpus with distractors, repeated runs, reindexing, lexical control, production regression, and inference reuse. Preserve each existing rank bound. |
| Complete page processing | Prove explicit completion, source revision binding, complete Unicode and final-character coverage, large values, native chunk coverage, and finite sparse embeddings at fixed bounds. |
| Authorization and input validation | Prove tenant/scope isolation, current source checks, structured filters, malformed input rejection, complete accepted input, and explicit engine/model/session/source errors. |
| Permission expansion | Prove opaque keys and renamed permission identifiers, filtering before ranking, final checks after index corruption, membership-only changes, access-only updates without inference, stale-writer rejection, and resumable policy transitions. |
| Distinct results and continuation | Traverse the duplicate-heavy corpus completely. Verify exact PIT sort values, bounded batches and summary reads, byte limits, empty continuing pages, replay, concurrent calls, process restart, and both deadlines. |
| Durable work and state lifecycle | Prove outage recovery, checkpoints, operational errors distinct from content exclusions, stuck-work reporting, generation safety, fair progress, fixed work bounds, and cleanup after convergence. |
| Index replacement lifecycle | Prove rebuild during mutations, projection refresh without replacement, access changes, native splits, failure recovery, single replacement, disk reserve, retirement, restore rebuilding, and restored-cursor rejection. |
| Cluster and capacity | Prove TLS, IPv6, discovery, model readiness/redeployment, two Tack processes, search outage recovery, search-only and mixed workloads, freshness, peak host memory, disk sizing, and separate production measurements. Claim scale-out or failover only after its required topology and failure tests. |

Use the current [test commands](../../../Makefile) and exact test selections instead of obsolete Graphite or retired deployment instructions in old plans. Run these commands separately against disposable local dependencies:

```sh
make test-integration
make test-search-group GROUP=G1
make test-search-group GROUP=G2
make test-search-group GROUP=G3
make test-search-group GROUP=G5
```

Run G4 and G6 with `TACK_SEARCH_CLUSTER=1` where the accepted cluster scenarios require them. Run the T1, T2, and T3 repeated groups after the corresponding correction. A skipped scenario does not supply evidence. Coordinate test resources before destructive test-environment cleanup.

TACK-574 measures public `ops qa datagen search-load` at the accepted offered rates. TACK-575 measures public `ops qa datagen search-mixed` plus the full accepted import, permission, rebuild, continuation, outage, and catch-up scenarios. Record offered, sent, completed, dropped, and failed requests separately from committed writes. Measure freshness, backlog age, latency percentiles, memory, CPU, and disk.

Use documented product targets for pass thresholds. Offered rates are measurement inputs. Report a missing product threshold explicitly rather than inventing one or treating an offered rate as a target. Do not claim search capacity from the OS-process throughput test or ordinary soak. Verify production capacity on its own host.

## 5. Preserve provider and shared deployment work

TACK-568 through TACK-573 retain the accepted [provider specification](../specs/2026-10-10-tack-provider.md). The related [shared guest contract](https://github.com/agoodkind/configs/blob/8bed552067f2ae61dac78cca8aececf5c30f0d0c/docs/superpowers/gueststate/spec.md) and [shared plan](https://github.com/agoodkind/configs/blob/8bed552067f2ae61dac78cca8aececf5c30f0d0c/docs/superpowers/gueststate/plan.md) govern Proxmox migration coordination. Recheck their reviewed revisions before apply.

1. Finish read-only application status and justified resource contracts, then implementation. Reuse existing providers for generic state. Preserve the required first-boot ordering and explicit destructive initialization guards.
2. Prove fresh deployment, import/adoption, external drift, interrupted apply, declared removal, locks, and secret handling on disposable systems. Require a subsequent plan without changes and a second apply without extra work.
3. Adopt QA resources before production and retire the prior writer only after each field has an established owner. General search resources must not independently switch Tack-owned aliases or delete its indexes.
4. Complete TACK-581 configuration integration and verify absent-guest handling on QA with the Proxmox owner. Source merge alone does not prove activation.

Continue independent coding while a shared QA guest or deployment lock waits. Do not treat a missing former helper or pending resource measurement as a reason to stop unrelated implementation.

## 6. Deploy and verify the reviewed correction

1. Coordinate one QA deployment window with `proxmox-guest-provider-migration`. Record the current guest membership, migration state, deployment lock, expected images, and provider ownership before changing configuration.
2. On QA, deploy the compatible audit, metadata, and repair prerequisites while existing ordinary-write checks remain active. Correct the fixed `scope_id` applicability contract and verify legitimate comment creation throughout the transition. Keep strict full-record enforcement inactive until cleanup completes.
3. Execute only reviewed QA source-data repairs and projection migration. Verify the complete manifest, retries, broad scan, nested-parent operations, native audit query, and affected content refresh. Then activate strict ordinary-write validation against the verified clean data. Test the scan-to-activation boundary with concurrent writes and reject activation when a new invalid write makes its evidence stale. Run the full acceptance checks on the resulting release.
4. After QA proves that sequence, deploy production under the existing authorization and locks. Apply a production source-data repair only after its concrete manifest receives approval, verify cleanup, and then enable strict validation. Recheck conditions that could invalidate the QA proof. Pending repair decisions delay strict-validation activation without blocking independent audit or search work.
5. Verify installed application and audit-consumer image digests, required store versions, model readiness, public queries, current authorization, continuation, audit deltas, and the fresh exclusion/stuck-work report. Classify every remaining exclusion; do not force a zero count by deleting unresolved content.
6. Complete the planned production recovery drill with source writes and pending search work. Earlier restart recovery does not establish that complete drill. Preserve backup/restore requirements and the already completed restricted-login and sequential-restart work.
7. Record PR, merge, configuration apply, installed image, backfill, workload, and runtime results separately in the responsible tickets. Close superseded tickets only after scope and evidence reconciliation. Preserve unverified acceptance and unapproved records as open work.

If a correction fails, stop its dependent deployment, preserve authoritative data, and restore the previously verified configuration or release only when that rollback is compatible with the applied migrations. Resume durable indexing from its recorded state. Do not use an application rollback to undo repaired source values or rewrite audit history.
