# OpenSearch search acceptance criteria

These criteria verify the [search architecture](2026-09-19-search-design.md). The OpenSearch release must pass real single-page and multi-page behavior with current storage. Storage expansion must rerun the same suite at larger node sizes.

## Evidence and environment

Tests use public operations with real FoundationDB, OpenSearch, the deployed model, and authentication. Mocks, recorded responses, private helpers, and source inspection do not establish acceptance. Engine traces and profiles provide supporting evidence.

Record Tack and configs revisions, image digests, model and tokenizer checksums,
index settings, shard counts, byte and work bounds, fixture identities, workload,
and measurements. QA must pass before production.

- Pin `github.com/opensearch-project/opensearch-go/v4` v4.7.3. Run the production adapter against `opensearchproject/opensearch:3.8.0`. Exercise typed index creation, settings, split, bulk, refresh, point-in-time creation and deletion, search request construction, alias changes, index and document reads, index deletion, metrics, routing, and close. Verify that the narrow search decoder preserves replacement PIT IDs and exact sort JSON.
- Exercise concrete ML Commons `opensearch.Request` types through `opensearch.Do` and `opensearch.ParseError`. Reject another HTTP client, generic method-and-path API, temporary v5 preview dependency, custom route selection, retry loop, connection pool, or error decoder.

## Build and lint gates

- Run `make build` on the exact implementation base before the first code edit. Stop if the untouched base fails under the current fetched go-makefile and shared `golangci-lint` configuration.
- Keep every new production declaration reachable from a real production entry point within the task that adds it. Do not commit interfaces, adapters, constructors, helpers, or exports for a later task to connect.
- Run `make build` after each coding task and before committing. It runs every repository gate and compiles the server. Fix each new finding in that task. The final validation plan reruns `make build` from a clean checkout before live tests.
- Do not edit lint baseline files or run an `accept-new` baseline target. New lint, complexity, strict-analyzer, and dead-code findings must remain zero.
- Design production code around the enforced rules from its first task: use concrete types, injected clocks, contextual logging, returned errors, and recovered goroutines. Do not introduce `any`, empty interfaces, `panic`, direct `time.Now`, `context.TODO`, unprotected goroutines, or `//nolint` suppressions.

## Meilisearch removal and temporary search outage

- Ship removal before any OpenSearch application path. Delete the Meilisearch client, adapters, dependency, configuration, startup setup, indexing hooks, batch reindex operation, Meilisearch test environment, deployed service, credentials, and operational documentation.
- Keep `tack_search` registered. Call it with an exact node reference, an exact title, ordinary words, and filters. Every call must return exactly `Search is temporarily unavailable.` The response must not mention either search engine, replacement work, or future availability.
- Run one real scenario for every registered non-search MCP tool with real FoundationDB. Assert that the scenario list exactly matches the non-search tool registry. Every scenario must work while every `tack_search` call returns the temporary unavailable response.
- Remove successful-search assertions from QA data generation and soak checks for this release. Preserve non-search coverage and assert the exact unavailable response. Build and start Tack without a Meilisearch endpoint, key, client library, service, image, or runtime dependency.
- Inspect rendered QA and production configuration and the live deployments. Neither environment may contain a Meilisearch process, container, service, secret, endpoint, or dependency. Treat deletion of the old volume as a separate authorized operation. Its presence must not start or configure Meilisearch.
- Commit creates, edits, and deletes during the outage. A later release must provision an empty OpenSearch index, rebuild only from FoundationDB, and make those mutations searchable before replacing the temporary response.
- Do not export, translate, import, attach, read, or inspect a Meilisearch index, snapshot, dump, volume, document, schema, synonym, ranking setting, or result as an input to OpenSearch provisioning, fixtures, relevance checks, or rebuilds.
- After OpenSearch activation, run the remaining public search acceptance criteria and recovery procedures. FoundationDB must remain the only rebuild source.

## Opaque metadata

- Require every property definition to declare search inclusion or exclusion.
  Reject new definitions without that declaration. Do not derive it from the
  property name, property type, or FoundationDB `Indexed` flag.
- Update built-in seeds and QA data generation with explicit declarations. Search
  behavior must remain identical when tests replace all identifiers and omit seeds.
- Run the expiring one-time backfill in dry-run mode against existing metadata.
  Require a complete manifest, no writes, bounded output, and an error for missing,
  duplicate, unknown, or conflicting entries. Then execute the same manifest twice
  and require the second run to make no changes.
- Refuse the first OpenSearch rebuild and public search while any property
  definition lacks a declaration. Verify zero missing definitions in QA and
  production before each environment's first rebuild.
- Load only the metadata and authentication required for the test. Load no product
  seeds. Create unfamiliar node types, property types, property identifiers, and
  hierarchy definitions through public operations.
- Define included, excluded, absent, scalar, structured, and labeled values. Search
  must index only declared text. Name-only nodes must remain searchable.
- Replace every type and property identifier. Preserve the declared behavior,
  coverage, relative ranks, and type filtering.
- Add a type after startup. Search it without changing or restarting application
  code. Change a projection and verify automatic reindexing against the same
  physical index and alias. Require new semantic fields only for affected pages,
  byte-identical semantic fields for unaffected pages, and retirement of obsolete
  affected-page document IDs.
- Omit or corrupt a required declaration. Tack must report the node and property.
  Repairing metadata must permit retry without an application change.
- Add many opaque properties. OpenSearch must retain the fixed mapping fields.

## Semantic relevance

Use one fixed corpus with at least 150 plausible distractor nodes. Repeat each query
three times and after reindexing. Every target must appear within the first 25
distinct nodes without query-specific rules or maintained synonyms.

| Query | Relevant node text |
| --- | --- |
| `db` | Database failover |
| `signin` | Authentication failure |
| `lag` | Slow request processing |
| `invoice` | Billing reconciliation |
| `crash` | Application terminated unexpectedly |
| `remove user` | Delete account |

A lexical-only control must miss at least one non-overlapping pair that the combined query retrieves. Capture query inference once and prove every continuation reuses the identical opaque token-weight map. The high-level semantic query cannot replace saved `query_tokens` unless it passes every relevance case and continuation latency threshold without repeating inference. Preserve a failing analyzer case and the measured repeated-inference regression.

## Complete page processing

- Use the real reader with a small page-byte setting. Indexing must start before the
  final page is read and continue until the explicit completion value. Change page
  counts between reads without changing search configuration.
- Put targets at the beginning, middle, page boundary, and final page. Include a
  name and value spanning many pages. A final-page semantic target must rank within
  the first 25 distinct nodes.
- Inspect indexed documents. Every source page must have the expected node, revision, projection, and ordinal. Every nonempty page must retain complete `page_text` and contain OpenSearch-generated `page_text_semantic_info.chunks.embedding` `rank_features` output.
- Inspect the native semantic field's generated chunks and embeddings through OpenSearch. Require bounded forward progress, complete final-character coverage, and one sparse embedding per generated nonempty chunk. Do not reproduce OpenSearch tokenizer logic.
- Repeat with emoji, combining marks, non-Latin text, Markdown, long URLs, long
  uninterrupted strings, punctuation, and whitespace. Exercise a valid 4,096-byte
  page and a newline-only page. Require valid Unicode, forward progress, one sparse
  result per generated input, and complete final-character coverage.
- Pin the native 160-character limit, 0.5 overlap, and unlimited chunk count. Repeat the proof after any model, semantic mapping, or bound change.

## Authorization and input validation

- Create matching nodes in two organizations and sibling scopes. Search must return only nodes allowed by resolved membership, entry point, scope, and optional type.
- Capture the query. It must apply access, type, and retirement filters as structured JSON. Quotes and JSON-like input cannot change filter meaning.
- Corrupt indexed access keys. Current authoritative checks must still withhold the node, including UUID-like queries and stale cursors.
- Empty and oversized queries must fail before inference. Every accepted query must be processed completely. Missing models, invalid mappings, unavailable engines, deleted points in time, and failed authoritative reads must return explicit errors.

## Permission expansion

- Use one permission boundary for indexed access keys and caller key construction. Store only `access.versions`, `access.keys`, and `access.generation`. Reject a policy that requires another OpenSearch field.
- Replace every permission node and relationship identifier in the fixture. Search results must remain identical. Search code must not inspect permission types, roles, groups, organizations, or scopes.
- Change one principal membership relationship. A new search must use the new caller keys without writing an OpenSearch document. An established session may keep its point in time, but the final FoundationDB check must use current permission state.
- Change one resource grant and one inherited grant. Update only `search_generation` and `access` on every current page. Preserve document IDs, `page_text`, generated chunks, and sparse weights byte for byte. Undeploy the model and require the update to succeed.
- Pause an older content write, finish a newer access update, then resume the content write. Repeat with an older access update and newer content. OpenSearch must reject both stale operations. Retrying the current generation must be idempotent.
- Start a candidate policy version for one authority while the active version serves queries. New writes must include both versions. Interrupt and resume the access scan. Change a resource grant during verification and require the affected scan and exact verification to repeat before activation. Transition another authority concurrently without sharing rollout state. Keep old sessions usable, then remove old keys after those authority-specific sessions finish.
- The version transition must not create an index, switch an alias, read text pages, or change sparse weights. Failure before activation must preserve the active version. A rollback before old-key cleanup must reactivate the previous version without document recovery.
- Before a new policy ships, prove that OpenSearch filters a corpus dominated by forbidden matches before ranking. Post-search filtering alone fails acceptance. Corrupt indexed access keys and require the final FoundationDB check to prevent disclosure.

## Distinct results and continuation

- Create 1,501 matching nodes. Give one node 36,000 matching page documents across
  three primary shards. Traverse every cursor with engine batches of at most 100.
  Require every node exactly once and stop only after an empty raw engine batch.
- Capture the sort values. They must contain score, node ID, and `_shard_doc` under
  one point in time. Equal score and node ID values must not skip page documents.
- Profile the query. Require `FeatureQuery` operations over `rank_features`. Reject
  dense scripts, nearest-neighbor `k`, hybrid result windows, and total-result caps.
- Open a point in time, then create, edit, and delete pages. The established traversal
  must remain unchanged. A new session must observe the changes.
- Place deleted and unauthorized candidates before valid nodes. Search must omit
  them and continue within the four-batch response budget.
- Trigger response bytes before 25 nodes. Preserve the first unconsumed candidate. Load each OpenSearch batch through one bounded FoundationDB summary operation. Four engine batches may use at most four summary operations. Replay must use the same batch path.
- Force four batches containing only visited pages. Return an empty public page with
  a continuation, then return the remaining nodes.
- Retry a lost response, concurrent cursor calls, and process restart. Committed
  pages must replay once. Session writes and memory must remain bounded.
- Verify the 15-minute inactivity deadline and two-hour absolute deadline. Neither
  replay nor continued activity may extend the absolute deadline.

## Durable work and state lifecycle

- Stop OpenSearch, commit creates, edits, deletes, metadata changes, and subtree
  moves, then restart. Durable work must converge without another edit or rebuild.
- Interrupt before and after page checkpoints, refresh, retirement, and partial bulk
  failure. Resume from the saved cursor. Failed pages must remain pending.
- Pause an old writer, finish a newer edit or deletion, then resume the old request.
  Old text must not reappear.
- Give one node enough pages to exceed a work slice. Each claim must stop admitting
  work after 32 pages, 5 MiB, or the two-second start deadline. Delay one engine
  request past two seconds. The worker must start no later operation, must stop the
  request at ten seconds, and must checkpoint before its next claim. Other live nodes
  must make progress. Cleanup must yield after 100 document IDs.
- Run live mutations, cleanup, a metadata rescan, and a rebuild together. Measure
  oldest work age for each class. No class may consume every worker.
- After convergence and session expiry, count FDB records by search key family.
  Repeat many edits. Completed work, old generations, issued IDs, replay records,
  and rebuild journals must be removed. State must not grow with edit history.

## Index replacement lifecycle

- Run a full FoundationDB replacement while creates, edits, deletes, metadata changes, and subtree moves continue. Fail scan, inference, replay, validation, and alias switching separately. The serving alias must remain correct and recovery must resume.
- Change a text projection without starting replacement. Require the serving physical index and alias to remain unchanged. Require bounded content work to reread and reembed only affected pages and retire their obsolete document IDs.
- Change the permission-policy version during replacement. Its access-only transition must complete on the serving index without starting another replacement. The journal must copy active and candidate keys into the target before alias switching.
- Create the initial index with a reserved routing-shard count divisible by every approved split target. Reject a lower or nonmultiplicative primary count before blocking engine writes.
- Increase only the primary count through the typed Split Index API. Keep the alias on the readable source while it is write-blocked. FoundationDB mutations must commit and remain queued.
- Undeploy the document model before splitting. Require identical mappings, documents, generated chunks, sparse weights, and saved raw-sparse query results. Rerun relevance and continuation acceptance without requiring equal numeric scores. Shard-local term statistics can change combined lexical scores.
- Exercise the complete reserved path from one primary shard through two, four, and eight. Require green targets and no FoundationDB node scan.
- Fail write blocking, split creation, replay, validation, and alias switching separately. Recovery must restore source writes, preserve the old alias, clean failed targets, and resume from durable state.
- Attempt two replacements. Exactly one may run. Require no more than one serving, one replacement, and one retiring index at every checkpoint.
- Configure low retired-page, retirement-age, and index-byte thresholds. Each threshold must start a full FoundationDB replacement because splitting preserves obsolete documents. Exhaust reserved disk and require pending engine work while source writes remain durable.
- Keep a session open on the old index. New sessions must use the new index. Bounded cleanup must delete the old index and retirement records after the absolute session deadline.
- Restore a FoundationDB backup into a disposable environment. Create a new search generation and empty index. Restored cursors must fail. Rebuilt search must include current nodes, exclude deleted nodes, and pass relevance and final-page checks.

## Cluster and capacity

- Verify one QA LXC and one production LXC with OpenSearch 3.8.0, verified REST
  and transport TLS, IPv6-only reachability, and least-privilege application
  credentials. Both environments start with zero replicas. Production must use
  normal cluster discovery rather than `discovery.type: single-node`.
- Require at least 8 GiB for every guest that runs the model. The final GTE sparse
  workload failed at 4 GiB, passed at 8 GiB, and used about 3.4 GiB afterward.
  Record actual JVM, model, process, filesystem, and peak host memory.
- Stop each environment's search guest during active source writes. FoundationDB
  writes must commit, public search must return an explicit unavailable error, and
  durable search work must remain pending. Restart the guest and require the backlog
  to become searchable.
- Configure one HTTPS search endpoint per environment on the hypervisor's guest-segment address. Require the official client's `ConnectionObserver` to record only that endpoint. Verify the endpoint certificate and require the proxy to verify every backend certificate. Each environment starts with one backend.
- Run a fixed production happy-path query count before application cutover. Proxy access records must show the single healthy production backend accepting requests. Stop it during source writes, require an explicit outage while FoundationDB commits, then require authenticated readiness and backlog recovery after restart.
- Deploy the model without `node_ids`. Require ML Commons to report `DEPLOYED` on each initial node after provisioning and restart. Keep native automatic redeployment enabled.
- Before production adds capacity, join the new members through the existing cluster without another initial bootstrap. Add their addresses to the proxy and require each healthy backend to accept requests. Change the replica count from zero to one only after at least two data nodes join, wait for green health, and record the new count with the physical generation. Require existing sparse weights to remain unchanged. Split primaries through the reserved routing path only when measured load requires it. With at least three members and one replica, stop each member in turn and require the endpoint, every primary, and local inference to remain available.
- Run one QA search session across two Tack processes by alternating every request.
  Require exact continuation, replay, authorization, and cleanup without sticky routing.
- Verify session and work keys use stable hash buckets and bounded bucket scans. A
  fixed workload must not serialize on one counter, lease, queue, or key range.
- Predeclare corpus size, page distribution, query mix, concurrency, indexing rate,
  rebuild activity, and pass thresholds for p50, p95, error rate, throughput, oldest
  work age, peak memory, and disk. Run the complete workload on the single QA node.
- Suburban provides 31.31 GiB usable memory, eight logical CPUs, and 215.92 GiB available fast storage. The one-week minimum available memory was 10.22 GiB. Keep at least 6.26 GiB available during the QA workload. The earlier 3.4 GiB post-workload reading plus the 0.125 GiB proxy budget would leave about 6.69 GiB, but it did not establish peak use. Do not leave the QA guest enabled unless the complete workload passes this live memory gate.
- One QA guest requires two logical CPUs and one 40 GiB fast disk. The measured CPU projection requires 5.43 total logical CPUs with reserve, and the host provides eight. The disk leaves 175.92 GiB, or 38.46 percent of the fast pool, available.
- Neither initial environment claims OpenSearch node failover or horizontal scale. Do not create temporary ML-only or data-only QA nodes. Measure production capacity on `vault`; do not reuse suburban measurements. Require the scale-out checks above before claiming production failover or increased OpenSearch capacity.
- Before removing storage limits, test 128 KiB, 1 MiB, 8 MiB, over 100 MB, and nodes
  larger than worker memory. Keep page and work bounds fixed.
- Add a Tack process and FoundationDB capacity independently in QA. Require the fixed
  request and worker workloads to improve without changing session or work formats.
- Measure non-index disk use after provisioning and primary index bytes after rebuilding. Each initial environment requires its disk to be at least `(non-index bytes + 3 * primary-index bytes) / 0.85` because one node stores three generations without a replica. Recalculate per-node disk before adding replicas because OpenSearch redistributes primary and replica shards across the expanded cluster. Capacity results, not model download size or one successful request, determine production sizing.
