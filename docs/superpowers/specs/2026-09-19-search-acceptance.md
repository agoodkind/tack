# OpenSearch search acceptance criteria

These criteria verify the [search architecture](2026-09-19-search-design.md)
through observable caller behavior and persisted state. A deployment is ready
only when its evidence demonstrates the complete contract.

## Evidence and environment

Automated tests use public MCP or operations calls with real FoundationDB,
OpenSearch, the deployed model, and authentication in local test containers.
Mocks, recorded responses, and private-helper tests do not establish acceptance.
Engine APIs and traces provide supporting evidence after public operations.

The release must pass QA validation before production deployment. Destructive
scenarios use disposable local or QA fixtures. Production verification checks artifacts, topology,
authorization, and authorized smoke fixtures without destroying production data.
New public search behavior also receives QA data-generator coverage.

Record the Tack and configs revisions, container digests, model and tokenizer
checksums, page and batch bounds, index settings, fixture identities, and measurements.
A passing source test alone does not prove deployment.

## Opaque types and metadata

- Initialize only the metadata and authentication needed to use Tack. Load no
  product seeds. Create node types, property types, properties, and hierarchy
  definitions with fresh opaque identifiers through public metadata operations.
  Define their value structures and text projections in metadata.
- Add distinct phrases to declared text in scalar and structured values.
  Public search finds each phrase under these unfamiliar types without changing
  or restarting application code. Repeat with a new type after indexing starts.
- Change every type and property identifier while preserving the declared
  meanings and values. Text coverage and relative ranking remain equivalent.
  Type-filter results follow the new identifiers. No fixture uses a known
  product type to obtain special behavior.
- Declare included, excluded, and inapplicable values, with absent and populated
  instances. Only declared contributions appear in indexed text. Name-only
  nodes remain searchable. Reorder maps and equivalent metadata input; the
  projected text remains equivalent. Retrying the same source revision and
  projection version produces identical page IDs.
- Change a projection declaration or its display text through public metadata
  operations. Affected nodes reindex automatically. Exact keyword probes find
  newly included text and stop matching removed text after convergence.
- Omit or invalidate a required projection declaration on an unfamiliar type.
  Tack returns an explicit error rather than silently omitting its values.
  Correct the metadata and verify indexing resumes without an application change.
- Add many opaque property identifiers. The OpenSearch mapping retains its
  fixed fields. Corrupt a declared value in a disposable storage fixture and
  invoke reindexing; the error identifies the node and property. Repairing the
  fixture permits retry without publishing a malformed replacement.

## Semantic relevance

Use one fixed corpus with at least 150 plausible distractor nodes. Record each
expected result's rank under the release configuration. Repeat after reindexing
with identical input and configuration. Each expected node appears in the first
25 results without query-specific rules or maintained synonyms.
All corpus text is ordinary fixture content under the opaque definitions above;
none of these phrases defines a node type, property, or special search behavior.

| Query | Relevant node text |
| --- | --- |
| `db` | Database failover |
| `signin` | Authentication failure |
| `lag` | Slow request processing |
| `invoice` | Billing reconciliation |
| `crash` | Application terminated unexpectedly |
| `remove user` | Delete account |

A controlled lexical-only query must miss at least one non-overlapping pair
that the production hybrid request retrieves. This proves semantic contribution
rather than a fixture that passes because every node fits on one page.
Do not require unrelated meanings of an ambiguous word to match.

## Paginated content and embedding coverage

- Through public storage operations, create and edit nodes at 128 KiB, 1 MiB,
  and 8 MiB, then nodes larger than OpenSearch's default 100 MB request limit.
  Grow a fixture beyond the search worker's memory budget. These sizes are
  test cases, not accepted node-size ceilings. Use real paginated storage;
  a size rejection or simulated reader does not pass this test.
- Put semantic targets near the beginning, middle, across page boundaries, and
  at the end. Include one property value and one node name that each span many
  pages. A `crash` query finds `Application terminated unexpectedly` present only
  in the final page among distractors, within the first 25 distinct results.
- Inspect public-operation traces and indexed documents. Each source page has
  its own document with the correct node ID, revision, projection version, and
  ordinal. Each contains finite 384-dimensional nested embeddings generated by
  OpenSearch. No request or in-memory collection contains the complete node.
- Verify that pages collectively include every declared text span, including
  the final character. Verify model inputs against the deployed model's actual
  tokenizer and truncation behavior. Stored page text and vector counts alone
  do not prove complete embedding coverage. Any tokenizer inspection belongs
  to validation tooling, never Tack's production indexing or query code.
- Repeat with emoji, combining characters, non-Latin text, Markdown, long URLs,
  long uninterrupted strings, and multi-megabyte whitespace spans. Exercise a
  page requiring more than 100 native chunks. No limit may append unprocessed
  text to a final oversized chunk. Require valid Unicode, forward progress,
  complete name coverage, and no silent model truncation.
- Record coverage failures against the actual native splitter and model.
  Pagination alone cannot pass this acceptance criterion. A failing native
  configuration blocks release; adding a tokenizer to Tack is not a valid fix.

## Authorization and query validation

- Create matching nodes in two organizations and sibling scopes. Search returns
  only nodes allowed by the authenticated caller's resolved entry point, scope,
  and optional node type. Repeat at several hierarchy depths using only opaque
  definitions created by the test.
- Capture both ranking branches. Each contains the same validated organization
  and scope filters as structured JSON. Quotes and JSON-like input cannot
  change filter meaning. Undefined type identifiers and foreign scopes are
  rejected before a ranking request is sent. Newly declared types are accepted.
- Deliberately corrupt a fixture's indexed organization or scope metadata.
  Public search still withholds the node when authoritative checks reject it.
  Repeat for an exact UUID or human-reference query and for a stale cursor
  after membership revocation or subtree movement.
- Empty queries return a recoverable error. Probe the deployed model's query
  limit from below and above. Each query is processed in full or explicitly
  rejected without token counting in Tack. A missing or mismatched model,
  unavailable engine, or failed authoritative read returns an explicit error.

## Distinct results and continuation

- Create 149 ordinary nodes and one node with more than 1,000 matching pages
  under one scope. Give each node the same unique search phrase. With small
  result summaries, paging returns six pages of 25 distinct nodes, covering
  every fixture exactly once. Repeat across all three primary shards.
- Place deleted and unauthorized candidates ahead of valid results. Search
  omits those nodes and refills pages where eligible nodes remain.
- Index overlapping revisions during an update. Search returns each node at
  most once with current authorized content, even if an old page ranks it.
- Store nodes large enough to trigger storage-batch and response-byte bounds.
  Results contain bounded current summaries, and continuation returns every
  unreturned candidate exactly once. Rendering and authorization never reconstruct
  full nodes. Paginated node retrieval returns the complete content.
- Continue a query while scores or indexed content change. Its established node
  order remains fixed, and authorization is checked again. A cursor used with
  another query, scope, principal, or incompatible index version is rejected.
- Expired cursors return a recoverable restart error. A query with more than
  1,000 eligible nodes reports the ranked-set limit; it does not claim that
  1,000 is an exhaustive count. Page counts are never shown as node counts.

## Durable changes and recovery

- Stop OpenSearch, commit node creates, updates, and deletes, then restart both
  the application and index workers. Pending operations survive and converge
  without another user edit or manual full rebuild.
- Interrupt a large update before and after saving page progress, during a
  partially failed bulk request, and during cleanup. Resume from durable progress
  without rereading the entire node. Repeated page IDs create no duplicates;
  failed pages remain pending. Shorten the node and verify excess old pages
  disappear. Delete it and verify every page is eventually removed.
- Edit the node while an older revision is being read. Every indexed revision
  contains pages from one committed source revision and fixed projection metadata.
  Make the old revision unavailable; require an explicit restart and bounded
  cleanup. Missing or corrupt pages must never be accepted as the end of a node.
- Delay an older update until a newer update or deletion finishes. Resume it.
  It cannot replace the newer revision or resurrect deleted documents.
- Move a subtree and update searchable metadata. Indexing updates every affected
  node, including descendants. Current authorization applies during catch-up.
- After convergence, rename or remove a distinctive phrase. An exact lexical
  probe no longer matches its obsolete pages. Do not require an approximate
  semantic query to return zero merely because that phrase was removed.
- Corrupt a stored chunk in a disposable fixture and request reindexing through
  the operations boundary. The paginated read fails explicitly, the serving alias
  stays unchanged, and no partial replacement is declared complete.
- Rebuild while public creates, updates, and deletes continue. After alias
  switching, the new index reflects every mutation through the handoff boundary
  and subsequent live indexing. Fail the scan, embedding, and catch-up stages
  separately; each failure preserves the serving index and permits a retry.
- Restore a FoundationDB backup into a disposable environment and rebuild an
  empty index. Verify current nodes, deleted-node absence, semantic relevance,
  and the final-page fixture from the restored source.

## Cluster and resource bounds

- Verify three independent LXCs in each environment, the specified per-guest
  resources, image version, verified REST and transport TLS, and IPv6-only
  reachability. Application credentials cannot perform provisioning operations.
- Stop each QA search guest in turn. Public searches and node writes succeed
  within the configured request timeout. A small newly created node becomes
  searchable within 10 seconds. Every primary remains assigned, inference
  remains available, and no shard shares its guest with its replica.
- Verify model provisioning can repeat without duplicate registrations and
  that a guest restart restores inference. Deny outbound model-download access
  after provisioning; ordinary local inference remains available.
- Index the large fixtures concurrently with queries and a full rebuild.
  Record p50 and p95 latency, peak JVM and native memory, CPU, disk, indexing
  throughput, queue depth, and oldest pending-work age. Keep page size and
  concurrency fixed while increasing total node size beyond worker memory.
  First-page indexing starts before the final page is read. Peak memory remains
  within the configured budgets; indexing time and disk use may grow with text.
- Inspect reads, bulk requests, and cleanup operations for byte and count bounds,
  including repeated metadata, JSON encoding, and action lines. Inject
  backpressure. Work remains durable without accumulating all pages in memory.
- Add a QA search node and verify shard redistribution without a Tack routing
  change. Confirm sufficient disk for the serving and replacement indexes.
  Production sizing requires measured capacity for the declared workload,
  including guest failure and rebuild, rather than the model's download size.
