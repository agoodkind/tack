# OpenSearch search acceptance criteria

These criteria verify the [search architecture](2026-09-19-search-design.md).
The first release must pass real single-page and multi-page behavior with current
storage. Storage expansion must rerun the same suite at larger node sizes.

## Evidence and environment

Tests use public operations with real FoundationDB, OpenSearch, the deployed model,
and authentication. Mocks, recorded responses, private helpers, and source inspection
do not establish acceptance. Engine traces and profiles provide supporting evidence.

Record Tack and configs revisions, image digests, model and tokenizer checksums,
index settings, shard counts, byte and work bounds, fixture identities, workload,
and measurements. QA must pass before production.

- Pin `github.com/opensearch-project/opensearch-go/v4` v4.7.3. Run the production adapter against `opensearchproject/opensearch:3.8.0`. Exercise typed index creation, split, bulk, refresh, point-in-time creation and deletion, search request construction, alias changes, index and document reads, index deletion, metrics, routing, and close. Verify that the narrow search decoder preserves replacement PIT IDs and exact sort JSON.
- Exercise concrete ML Commons `opensearch.Request` types through `opensearch.Do` and `opensearch.ParseError`. Reject another HTTP client, generic method-and-path API, temporary v5 preview dependency, custom route selection, retry loop, connection pool, or error decoder.

## Meilisearch removal

- Build and start Tack without a Meilisearch service, image, volume, endpoint,
  key, client library, or runtime dependency.
- Commit node creates, edits, and deletes through real FoundationDB. Require the
  OpenSearch workers to index each change through public operations.
- Stop OpenSearch. Source writes must still commit, and search must return an
  explicit unavailable error instead of reporting success through a no-op client.
- Provision an empty OpenSearch index and rebuild it only from FoundationDB. The
  provisioning and rebuild operations must not read or transfer Meilisearch data.
- Inspect rendered QA and production configuration and the live deployments.
  Neither environment may contain a Meilisearch process, container, secret,
  endpoint, volume, or dependency.
- Run the current recovery and operator procedures. Every search operation must
  use OpenSearch, and recovery must treat FoundationDB as the only source.

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
- Replace every type and property identifier while keeping the declared behavior.
  Coverage, relative ranks, and type filtering must remain equivalent.
- Add a type after startup. Search it without changing or restarting application
  code. Change a projection and verify automatic reindexing.
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

- Create matching nodes in two organizations and sibling scopes. Search must return
  only nodes allowed by resolved membership, entry point, scope, and optional type.
- Capture the query. It must apply organization, scope, type, and retirement filters
  as structured JSON. Quotes and JSON-like input cannot change filter meaning.
- Corrupt indexed organization or scope values. Current authoritative checks must
  still withhold the node, including UUID-like queries and stale cursors.
- Empty and oversized queries must fail before inference. Every accepted query must
  be processed completely. Missing models, invalid mappings, unavailable engines,
  deleted points in time, and failed authoritative reads must return explicit errors.

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
- Trigger response bytes before 25 nodes. Preserve the first unconsumed candidate.
  Rendering and authorization may read only bounded current summaries.
- Force four batches containing only visited pages. Return an empty public page with
  a continuation, then reach the remaining nodes.
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
- Create the initial index with a reserved routing-shard count divisible by every approved split target. Reject a lower or nonmultiplicative primary count before blocking engine writes.
- Increase only the primary count through the typed Split Index API. Keep the alias on the readable source while it is write-blocked. FoundationDB mutations must commit and remain queued.
- Undeploy the document model before splitting. Require identical mappings, documents, generated chunks, sparse weights, and saved raw-sparse query results. Combined lexical scores may change with shard-local term statistics, so rerun relevance and continuation acceptance instead of requiring equal numeric scores.
- Exercise the complete reserved path from one primary shard through two, four, and eight. Require green targets and no FoundationDB node scan.
- Fail write blocking, split creation, replay, validation, and alias switching separately. Recovery must restore source writes, preserve the old alias, clean failed targets, and resume from durable state.
- Attempt two replacements. Exactly one may run. Require no more than one serving, one replacement, and one retiring index at every checkpoint.
- Configure low retired-page, retirement-age, and index-byte thresholds. Each threshold must start a full FoundationDB replacement because splitting preserves obsolete documents. Exhaust reserved disk and require pending engine work while source writes remain durable.
- Keep a session open on the old index. New sessions must use the new index. Bounded cleanup must delete the old index and retirement records after the absolute session deadline.
- Restore a FoundationDB backup into a disposable environment. Create a new search generation and empty index. Restored cursors must fail. Rebuilt search must include current nodes, exclude deleted nodes, and pass relevance and final-page checks.

## Cluster and capacity

- Verify three independent LXCs per environment, OpenSearch 3.8.0, verified REST and
  transport TLS, IPv6-only reachability, and least-privilege application credentials.
- Require at least 8 GiB per guest before QA. The final GTE sparse workload failed
  at 4 GiB, passed at 8 GiB, and used about 3.4 GiB afterward. Record actual JVM,
  model, process, and filesystem memory.
- Stop each QA guest in turn. Public search and writes must succeed. New small nodes
  must become searchable within 10 seconds. Every primary and local inference must
  remain available.
- Run a fixed happy-path query count and record selected connections through the official test-only `ConnectionObserver`. Every healthy node must accept work, and one node must not remain the sole coordinator. Do not require exact round-robin counts. Stop each node and require the official client to recover.
- Deploy the model without `node_ids`. Require ML Commons to report `DEPLOYED` on all three eligible `ml` nodes before and after each restart. Keep native automatic redeployment enabled.
- Run one search session across two Tack processes by alternating every request.
  Require exact continuation, replay, authorization, and cleanup without sticky routing.
- Verify session and work keys use stable hash buckets and bounded bucket scans. A
  fixed workload must not serialize on one counter, lease, queue, or key range.
- Predeclare corpus size, page distribution, query mix, concurrency, indexing rate,
  rebuild activity, and pass thresholds for p50, p95, error rate, throughput, oldest
  work age, peak memory, and disk. Run the workload normally and with one guest down.
- Saturate the role under test before each scale-out run. Predeclare the required
  throughput gain and require the added capacity to process measured work.
- Before removing storage limits, test 128 KiB, 1 MiB, 8 MiB, over 100 MB, and nodes
  larger than worker memory. Keep page and work bounds fixed.
- Add an ML-only QA node. Require automatic model deployment and improved new-session
  inference throughput without reindexing or application changes.
- Add a data-only QA node and another replica. Require improved ranking throughput
  without reindexing. Then split the same corpus to a higher primary-shard count and
  require improved indexing throughput without inference or a Tack routing change.
- Add a Tack process and FoundationDB capacity independently. Require the fixed
  request and worker workloads to improve without changing session or work formats.
  Remove temporary nodes only after work, model, and shard relocation complete.
- Require disk for the serving, replacement, and retiring indexes. Capacity results,
  not model download size or one successful request, determine production sizing.
