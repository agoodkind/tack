# Search acceptance criteria

Search releases must pass these criteria through public operations with real FoundationDB, OpenSearch, the deployed model, and authentication. Storage expansion must repeat them at larger node sizes. A criterion without observed evidence remains unverified. Search releases must also satisfy the [cluster and capacity criteria](cluster-capacity-criteria.md).

## Evidence and environment

- The acceptance test must record the exact Tack and Configs revisions, dirty files, image digests and architecture, model and tokenizer checksums, index settings, shard counts, byte and work bounds, fixture identities, corpus, workload, commands, exit codes, observations, and artifacts. The acceptance test must distinguish provisioning, deployment, public activation, behavior acceptance, capacity, and cleanup.
- Acceptance requires QA acceptance before production. Local tests, source inspection, private helper calls, mocks, recorded responses, engine traces, and profiles do not establish deployed acceptance. Traces and profiles can support evidence from public operations. Public fixtures must use the real bearer-token lookup and organization membership. The acceptance test must keep raw fixture credentials in memory, store only token hashes, use isolated test data, and clean up identities and data when the fixture ends. The acceptance test must use an ephemeral CA and server certificate for the disposable TLS fixture. The acceptance test must test both JSON and SSE responses with audit recording active.

The acceptance test must exercise the production official stable client against the pinned OpenSearch 3.8.0 engine. It must cover each operation:

- Typed creation.
- Settings.
- Split.
- Bulk indexing.
- Partial updates.
- Write blocking.
- Refresh.
- Point-in-time creation.
- Point-in-time deletion.
- Search construction.
- Alias changes.
- Index reads.
- Document reads.
- Deletion.
- Metrics.
- Routing.
- Close.

The acceptance test must preserve replacement point-in-time IDs and exact sort JSON. It must exercise concrete ML Commons requests through the official execution and error decoder. It must prove that OpenSearch rejects `version` and `version_type` on a bulk update. It must call the registered provision and verify operations against the real TLS fixture. It must vary model, mapping, alias, and topology expectations independently and require a concrete mismatch for each. It must reject custom HTTP transport, route selection, connection pools, error decoding, and application retry loops.

Acceptance requires complete permanent audit evidence for each operation class:

- Public search.
- Operator commands.
- Sessions.
- Indexing.
- Model repair.
- Replacement work.

The acceptance test must inspect every required audit field:

- Verified actors and initiating identities.
- Complete targets.
- Original time and committed order.
- Outcomes.
- Old and new field and relationship values, including removals.
- Read manifests.
- Returned counts.
- Selection criteria.
- Fingerprints.

The acceptance test must exercise authorization refusals, admission and finalization failures, partial work, interruptions, and recovery. The operation must withhold results or effects when required recording fails. Acceptance requires `runtime audit coverage` and applicable integration checks before merge. The acceptance test must verify accepted behavior on QA before release.

## Build and lint gates

- The developer must run `make build` on the exact unedited implementation base and after each coding task before commitment. The developer must stop on a base failure. The developer must use the current shared build and lint configuration and fix every new finding within its owning change.
- The developer must connect every new production declaration to a production entry point in the change that adds it. The developer must not commit future-only interfaces, constructors, helpers, adapters, or exports.
- The implementation must introduce zero new lint, complexity, strict-analyzer, and dead-code findings. The implementation must not edit baselines or use `accept-new` targets. The implementation must use concrete types, injected clocks, contextual logging, returned errors, and recovered goroutines. The implementation must not add `any`, empty interfaces, `panic`, direct `time.Now`, `context.TODO`, unprotected goroutines, or lint suppressions.
- Acceptance requires a clean final implementation build and all required real-dependency groups. Compilation, successful startup, a skipped scenario, and a green verification command do not establish behavior acceptance.

## Search removal boundary

- The acceptance test must verify that application and deployed configuration do not require a Meilisearch client, service, process, credentials, endpoint, startup hook, indexing hook, test fixture, fallback, compatibility layer, or old operator path. The acceptance test must inspect deployed state separately from source changes.
- The acceptance test must preserve FoundationDB as the only source for empty-index rebuilding, including creates, edits, and deletes committed during a search outage. The implementation must not read, inspect, export, translate, import, or reuse Meilisearch documents, schemas, settings, synonyms, rankings, results, snapshots, dumps, or volumes as replacement inputs.
- The acceptance test must treat any retained old volume as a separately authorized deletion. Its presence must not configure or start a service. The implementation must not rerun a completed historical removal outage to validate a correction.
- While public search is disabled, acceptance requires every ordinary, exact-reference, exact-title, filtered, malformed, and omitted-input call to return exactly `Search is temporarily unavailable.` without engine details or an availability estimate. Acceptance requires one real scenario for every registered non-search MCP tool and an exact registry-to-scenario match. Source writes to FoundationDB and SQL, non-search QA and soak scenarios, and those tools must work without a search service. The acceptance test must activate ranked search only after rebuilding and acceptance.

## Opaque metadata

- The acceptance test must load only required metadata and authentication. The acceptance test must omit product seeds. The acceptance test must create unfamiliar node types, property types, property identifiers, and hierarchy definitions through public operations. The acceptance test must replace all identifiers and require identical declared behavior, coverage, relative rank, and type filtering.
- The acceptance test must store definitions without declarations when their types supply defaults. The acceptance test must verify readable scalars, option labels, and default exclusions, including node references. Explicit inclusion and exclusion override defaults. Search must omit an explicitly excluded value beside an included value in the same fixture. The acceptance test must reject definitions without either a declaration or a default. Property names and `Indexed` must not choose inclusion.
- Acceptance requires explicit declarations in built-in seeds and QA data for reproducible fixtures. The acceptance test must test included, excluded, absent, scalar, structured, and labeled values. Acceptance requires name-only nodes to remain searchable.
- The acceptance test must dry-run the expiring projection backfill against a complete reviewed manifest. Acceptance requires bounded output and unchanged source state. The acceptance test must reject missing, duplicate, unknown, cross-organization, malformed, and conflicting entries. The acceptance test must execute the exact manifest twice and require an unchanged second run. The acceptance test must test concurrent explicit changes and partial-failure recovery. The acceptance test must verify that initial filling leaves ordinary search work unchanged.
- Acceptance requires readiness before provisioning, rebuilding, and verification. Each command must refuse invalid declarations or definitions without declarations or defaults. The acceptance test must verify readiness separately in QA and production before rebuilding.
- The acceptance test must add a type after startup and search it without restarting or changing application code. The acceptance test must change a projection against the same physical index and alias. Acceptance requires new semantic fields only for affected pages, byte-identical fields for unaffected pages, and retirement of obsolete affected IDs.
- The acceptance test must put an invalid value, an undeclared property, and an inapplicable definition beside valid content. Acceptance requires searchable names and valid text. The acceptance test must omit invalid values from indexed text and preserve them in source storage. Verification must warn with node, property, revision, projection, and reason without failing or excluding the node. The acceptance test must repair through the approved boundary, verify the repair, and require durable refresh to clear warnings. Deletion must clear warnings.
- The acceptance test must change renderer semantics without changing stored definitions. Acceptance requires old cursors to fail, pending work to resume under the new projection, all affected nodes to refresh, and explicit exclusions to remain intact. The acceptance test must stop FoundationDB during metadata refresh and require an explicit error. Concurrent type changes and requests must use complete metadata epochs. The acceptance test must verify that a deleted type disappears from the generated tools. Many opaque properties must preserve the fixed engine mapping.

## Semantic relevance

The acceptance test must use one fixed corpus with at least 150 plausible distractors. The acceptance test must repeat every query three times and after reindexing. Every target must appear within the first 25 distinct nodes without query-specific branches or maintained synonyms.

Each query must retrieve the corresponding text.

Query targets:

| Query | Relevant text |
| --- | --- |
| `db` | Database failover |
| `signin` | Authentication failure |
| `lag` | Slow request processing |
| `invoice` | Billing reconciliation |
| `crash` | Application terminated unexpectedly |
| `remove user` | Delete account |

- Acceptance requires a lexical-only control to miss at least one non-overlapping pair that the combined query retrieves. The acceptance test must save one query inference and prove byte-identical opaque `query_tokens` on every continuation and replay. A high-level semantic query can replace saved tokens only after it passes every relevance case and continuation latency threshold without repeated inference.
- The acceptance test must preserve the analyzer miss and repeated-inference comparison that justify saved tokens. The acceptance test must capture actual indexed text, model and tokenizer identities, generated sparse fields, query weights, structured filters, lexical boosts, and rank traces for any failure. The acceptance test must preserve the production wording and expected TACK-578 content as a public regression with plausible distractors. The acceptance test must resolve any ticket-to-corpus count mismatch before claiming complete coverage.

The acceptance test must prepare separate opaque cohorts with the required populations.

Opaque cohort populations:

| Cohort | Nodes |
| --- | ---: |
| Semantic | 162 |
| Continuation | 31 |
| Final-page | 1 |

The 31 eligible continuation nodes include one node with repeated matching text across multiple reader pages. Traversing the cohort takes at least two responses even with 25 nodes per page. The test must use the actual stored IDs and expected case IDs. A fixture manifest establishes preparation only.

## Complete page processing

- The acceptance test must use the real reader with a small byte bound. Acceptance requires indexing before the final read and continuation until explicit completion. The acceptance test must change page counts between reads without changing configuration. The acceptance test must reject changed, missing, or corrupt revisions before combining pages.
- The acceptance test must put targets at the beginning, middle, page boundary, and final page, with a name and value spanning many pages. Acceptance requires the final-page semantic target within the first 25 distinct nodes.
- The acceptance test must inspect every indexed page's node, revision, projection, and ordinal. Acceptance requires complete `page_text` and native `page_text_semantic_info.chunks.embedding` output for nonempty pages. The acceptance test must inspect generated chunks for valid Unicode, bounded forward progress, final-character coverage, and one finite sparse result per generated input. Whitespace-only chunks may contain an empty embedding map. The map must still exist.
- The acceptance test must repeat with emoji, combining marks, non-Latin text, Markdown, long URLs, uninterrupted strings, punctuation, whitespace, a valid 4,096-byte page, a newline-only page, active empty text, missing text, retirement records, and unknown fields. The acceptance test must reject missing required text and unknown mapped fields. The acceptance test must accept valid empty text without inference.

Native chunk coverage must use the accepted bounds.

Native chunk bounds:

| Subject | Bound |
| --- | --- |
| Chunk size | 160 characters. |
| Semantic overlap | Zero. |
| Chunk count | Unlimited. |

The acceptance test must repeat proof after a model, semantic mapping, or bound change. The implementation must not reproduce OpenSearch tokenizer logic.

Before storage-limit removal, the acceptance test must cover each size case while keeping page and work bounds fixed.

Required size cases:

| Case | Node size |
| --- | --- |
| 1 | 128 KiB. |
| 2 | 1 MiB. |
| 3 | 8 MiB. |
| 4 | Over 100 MB. |
| 5 | Larger than worker memory. |

## Authorization and input validation

- The acceptance test must create matching nodes in two organizations and sibling scopes. Acceptance requires resolved membership, entry-point, scope, and optional-type isolation. The acceptance test must preserve valid nested nodes and separate project scope. The acceptance test must exclude nodes when authoritative permission cannot be established.
- The acceptance test must capture structured access, type, and retirement filters. Quotes and JSON-like input must not alter their meaning. Empty and oversized queries must fail before inference, and accepted input must receive complete processing.
- The acceptance test must corrupt indexed keys and require current FoundationDB checks to withhold forbidden nodes, including UUID-like queries, stale cursors, continuation, and replay. Missing models, invalid mappings, unavailable engines, deleted points in time, and failed authoritative reads must return explicit errors.

## Permission expansion

- Acceptance requires one policy boundary for resource and caller keys, with only `access.versions`, `access.keys`, and `access.generation` in the engine mapping. The acceptance test must reject a policy that requires another permission field. The acceptance test must replace permission node and relationship identifiers without changing results. Search must not interpret permission types, roles, groups, organization IDs, or scope IDs in a key.
- The acceptance test must change principal membership and require new caller keys without document writes. Established snapshots may remain open, but final checks must use current permission state. Search and replay during pending revoked-permission work must not disclose forbidden nodes.
- The acceptance test must change direct and inherited resource grants. The acceptance test must update only `search_generation` and `access` on every current page. The acceptance test must preserve IDs, text, chunks, and weights byte for byte. The acceptance test must start with generation 1 and update to generation 2. The acceptance test must retry generation 1 and require `ErrObsoleteWrite`. The acceptance test must retry generation 2 and require unchanged content. The acceptance test must undeploy the document model and require success without content reads or inference.
- The acceptance test must pause an old content write across a new access update and an old access write across new content. The acceptance test must reject both stale operations. Acceptance requires the newest generation, text, and access to survive. The acceptance test must test a page change after the content precondition read and an idempotent retry of the current generation.
- The acceptance test must begin one authority's candidate while the active policy serves requests. New writes must include both versions. The acceptance test must interrupt and resume every scan and verification phase. The acceptance test must change a grant during verification and repeat the affected checks before activation. The acceptance test must transition another authority independently. The acceptance test must keep existing sessions usable and remove old keys after their authority-specific sessions finish.
- Acceptance requires unchanged physical index, alias, text, chunks, and weights throughout policy transitions. Failure before activation must preserve the active version. Rollback before retirement must reactivate the previous policy without document recovery. After retirement begins, acceptance requires another ordinary transition.
- Before shipping a policy, the acceptance test must use a corpus dominated by forbidden matches and prove that the engine filters them before ranking. Post-search filtering alone fails acceptance. The acceptance test must corrupt indexed keys separately to prove the final source check.

## Distinct results and continuation

The distinct-results test must exercise the required traversal population and batch limit.

Traversal criteria:

| Subject | Requirement |
| --- | --- |
| Matching nodes | 1,501. |
| Matching pages for one node | 36,000. |
| Primary shards | Three. |
| Batch size | At most 100. |
| Node coverage | Every eligible node exactly once. |

Traversal must end only after an empty raw batch. The test must preserve score, node ID, and `_shard_doc` sort values under one point in time. Equal scores and IDs must not skip pages.

- The acceptance test must profile sparse `FeatureQuery` operations over `rank_features`. The acceptance test must reject dense scripts, nearest-neighbor `k`, hybrid windows, collapse, and total-result caps.
- The acceptance test must open a snapshot before page creates, edits, and deletes. Acceptance requires stable established traversal and current results in a new session, subject to current deletion and permission checks before return.
- The acceptance test must put deleted and unauthorized candidates before valid ones. The acceptance test must continue within four batches. The acceptance test must trigger bytes before 25 nodes and preserve the unconsumed candidate. Four batches may use at most four summary operations. Replay must use that same bounded summary path.
- The acceptance test must force four duplicate-only batches. Acceptance requires an empty public page with continuation and later remaining nodes. The acceptance test must retry lost responses, concurrent calls, process restart, and process replacement. Acceptance requires committed-page replay, bounded session writes and memory, exact distinct results, and cleanup.
- The acceptance test must verify the two-minute inactivity deadline, renewed point-in-time keep-alive, expired-cursor snapshot deletion, and two-hour absolute deadline under the accepted deployment settings. Neither replay nor activity may extend the absolute deadline.

## Durable work and state lifecycle

- The acceptance test must stop OpenSearch during creates, edits, deletes, metadata changes, and subtree moves. Acceptance requires committed source writes, explicit search outage, pending durable work, and convergence after restart without another edit or rebuild. The acceptance test must allow five minutes after restart for the engine and model to become ready. From readiness, the public query must return the node created through MCP during the outage within ten seconds.
- The acceptance test must interrupt before and after page checkpoints, refresh, retirement, and partial bulk failure. The acceptance test must resume saved progress and preserve failed pages. The acceptance test must abort a source mutation and verify that neither the mutation nor its search work commits. The acceptance test must pause an old writer across a newer edit or deletion. Old text must not reappear.
- The acceptance test must rebuild with a parentless node. Acceptance requires completion, search exclusion, and a reason in verification without verification failure. The acceptance test must add a valid parent and require normal indexing and exclusion removal. Engine failures must not exclude nodes or count toward the source-failure attempt limit.
- The acceptance test must repeatedly fail FoundationDB reads for rebuild, cleanup, deleted-node, rescan, and rollout items. The acceptance test must continue retries. Beyond the attempt limit, verification must fail and report kind, ID, attempts, and last error. The worker must log that record once. Completion must clear the count.
- The acceptance test must use a node larger than one slice. The acceptance test must admit at most 32 pages, 5 MiB, or two seconds of new operations. The acceptance test must delay one request beyond two seconds. Acceptance requires the worker to stop admitting operations, cancel the request at ten seconds, and checkpoint before another claim. Acceptance requires progress for other live nodes. Cleanup and access work must yield after 100 IDs.
- The acceptance test must run live mutations, access work, cleanup, metadata rescans, rollouts, and rebuilding together. The acceptance test must record page reads, encoded bytes, slice duration, and peak memory. The acceptance test must measure oldest work age for each class and require fair progress. The acceptance test must increase workers under a fixed workload and require improvement without format changes.
- The acceptance test must count FoundationDB search records after convergence and session expiry, then repeat many edits. The acceptance test must remove completed work, obsolete generations, issued IDs, replay records, and obsolete replacement state. Acceptance requires state to remain independent of edit history.

## Index replacement lifecycle

- The acceptance test must rebuild during creates, edits, deletes, metadata changes, subtree moves, grants, and a candidate-policy transition. The acceptance test must fail scanning, inference, target catch-up, exact validation, and alias switching separately. Acceptance requires a correct serving alias and resumable recovery. The acceptance test must verify that concurrent writes update the serving and replacement indexes.
- The acceptance test must change text projection without replacement. Acceptance requires the same physical index and alias, affected-page inference only, unchanged unaffected fields, and obsolete-ID retirement. The acceptance test must transition access during replacement without starting another replacement. Acceptance requires active and candidate keys on the target before switching.
- The acceptance test must reserve routing shards divisible by all approved split targets. The acceptance test must reject lower or nonmultiplicative primary counts before blocking writes. The acceptance test must keep the source readable behind the alias while engine writes pause. FoundationDB must commit source mutations and retain the pending search work.
- The acceptance test must split from one to two, four, then eight primaries with the document model undeployed. Acceptance requires green targets, unchanged mappings, documents, generated chunks, weights, saved raw-sparse results, and absence of a FoundationDB node scan. The acceptance test must redeploy and rerun relevance and continuation. Combined lexical scores need not match because shard-local statistics can change.
- The acceptance test must fail write blocking, split creation, catch-up, validation, and alias switching separately. The acceptance test must restore source writes, preserve the old alias, remove failed targets, and resume durable work. The acceptance test must reconcile uncertain external outcomes from actual state.
- The acceptance test must attempt concurrent replacements and require exactly one. The acceptance test must permit at most one serving, one replacement, and one retiring index. The acceptance test must trigger each retired-page, retirement-age, and index-byte threshold and require full rebuilding. Automatic index creation must remain disabled. The acceptance test must exhaust reserved disk and require pending engine work while source writes remain durable.
- The acceptance test must keep a session on the old index while new sessions use the replacement. After the two-hour absolute deadline, bounded cleanup must confirm that the old index does not have any active sessions. Before deleting it, the operator must block writes, revoke write permissions, and disable automatic recreation. The acceptance test must delete the old index through the typed API. The acceptance test must confirm its deletion and cleanup of session and retirement records. The acceptance test must restore a real FoundationDB backup into a disposable environment, create a new restore epoch and empty index, reject restored cursors, and verify current, deleted, relevant, and final-page content.
