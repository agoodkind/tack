# OpenSearch search acceptance criteria

These criteria verify the [search architecture](2026-09-19-search-design.md)
through observable caller behavior and persisted state. A deployment is ready
only when its evidence demonstrates the complete contract.

## Evidence and environment

Automated tests enter through the public MCP or operations boundary and use
real FoundationDB, OpenSearch, the deployed embedding model, and authentication.
They run in the local container test environment. Mocks, recorded responses,
and private-helper tests do not establish acceptance. Engine APIs and traces
provide supporting evidence after public operations.

The same release passes QA validation before production deployment. Destructive
fault, corruption, rebuild, and load scenarios use disposable local or QA
fixtures. Production verification checks the deployed artifacts, topology,
authorization, and authorized smoke fixtures without destroying production data.
New public search behavior also receives QA data-generator coverage.

Record the Tack and configs revisions, container digests, model and tokenizer
checksums, index and pipeline settings, fixture identities, and measured results.
A passing source test alone does not prove deployment.

## Opaque types and metadata

- Initialize only the metadata and authentication needed to use Tack. Load no
  product seeds. Create node types, property types, properties, and hierarchy
  definitions with fresh opaque identifiers through public metadata operations.
  Define their value structures and text projections in metadata.
- Add distinct phrases to declared fragments of scalar and structured values.
  Public search finds each phrase under these unfamiliar types without changing
  or restarting application code. Repeat with a new type after indexing starts.
- Change every type and property identifier while preserving the declared
  meanings and values. Text coverage and relative ranking remain equivalent.
  Type-filter results follow the new identifiers. No fixture uses a known
  product type to obtain special behavior.
- Declare included, excluded, and inapplicable values, with absent and populated
  instances. Only declared contributions appear in indexed text. Name-only
  nodes remain searchable. Reorder maps and equivalent metadata input; the
  ordered fragments, generation, and passage IDs remain unchanged.
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

## Complete content coverage

- Through public create and update operations, store nodes below the temporary
  storage limit and, with TACK-525 enabled, serialized nodes at 128 KiB, 1 MiB,
  4 MiB, and 8 MiB. Put semantic targets near the beginning, middle, across a
  storage-chunk boundary, and at the end. Each target is retrievable.
- Put `Application terminated unexpectedly` only in the final stored chunk of
  the largest fixture. A `crash` search ranks the node in its first 25 results
  among distractors. Its generation contains more than 100 passage documents.
- Inspect every passage for contiguous ordinals, one finite 384-dimensional
  vector, and the permitted token length under the deployed tokenizer including
  special tokens. Verify complete source-span coverage without gaps. The last
  source character belongs to an embedded passage.
- Repeat with emoji, combining characters, non-Latin text, Markdown, long URLs,
  the largest accepted node name, and multi-megabyte whitespace spans. Verify
  both passage byte bounds and full-name coverage beyond the keyword prefix.
  No input causes truncation, invalid Unicode, omitted spans, or an overlap loop
  that fails to advance.
- Repeat indexing the same verified input. Document IDs and passage counts are
  unchanged. No extra generation appears because map iteration order changed.
- Before TACK-525 is available, oversized writes return TACK-524's recoverable
  size error. Search work is not recorded for a rejected mutation. An 8 MiB
  storage rejection cannot count as a passing large-content search test.

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
- Empty queries and queries one token above the specified limit return a
  recoverable error. A query at the limit is processed in full. A missing model,
  mismatched tokenizer, unavailable engine, or failed authoritative read returns
  an explicit error rather than an apparently successful empty result.

## Distinct results and continuation

- Create 149 ordinary nodes and one node with more than 1,000 matching passages
  under one scope. Give each node the same unique search phrase. With small
  result summaries, paging returns six pages of 25 distinct nodes, covering
  every fixture exactly once. Repeat across all three primary shards.
- Place deleted and unauthorized candidates ahead of valid results. Search
  omits those nodes and refills pages where eligible nodes remain.
- Index overlapping generations during an update. Search returns each node at
  most once with current authorized content, even if an old passage ranks it.
- Store nodes large enough to trigger storage-batch and response-byte bounds.
  Results contain bounded current summaries, and continuation returns every
  unreturned candidate exactly once. Public node retrieval returns full content.
- Continue a query while scores or indexed content change. Its established node
  order remains fixed, and authorization is checked again. A cursor used with
  another query, scope, principal, or incompatible index version is rejected.
- Expired cursors return a recoverable restart error. A query with more than
  1,000 eligible nodes reports the ranked-set limit; it does not claim that
  1,000 is an exhaustive count. Passage counts are never shown as node counts.

## Durable changes and recovery

- Stop OpenSearch, commit node creates, updates, and deletes, then restart both
  the application and index workers. Pending operations survive and converge
  without another user edit or manual full rebuild.
- Interrupt a large update between bulk requests and after indexing but before
  cleanup. The authoritative mutation remains intact. Retrying completes one
  projection and removes superseded passages without deleting the new ones.
- Delay an older update until a newer update or deletion finishes. Resume it.
  It cannot replace the newer generation or resurrect deleted documents.
- Move a subtree and update searchable metadata. Indexing updates every affected
  node, including descendants. Current authorization holds during catch-up.
- After convergence, rename or remove a distinctive phrase. An exact lexical
  probe no longer matches its obsolete passages. Do not require an approximate
  semantic query to return zero merely because that phrase was removed.
- Corrupt a stored chunk in a disposable fixture and request reindexing through
  the operations boundary. Reconstruction fails explicitly, the serving alias
  stays unchanged, and no partial replacement is declared complete.
- Rebuild while public creates, updates, and deletes continue. After alias
  switching, the new index reflects every mutation through the handoff boundary
  and subsequent live indexing. Fail the scan, embedding, and catch-up stages
  separately; each failure preserves the serving index and permits a retry.
- Restore a FoundationDB backup into a disposable environment and rebuild an
  empty index. Verify current nodes, deleted-node absence, semantic relevance,
  and the final-chunk fixture from the restored source.

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
  throughput, queue depth, and oldest pending-work age. Every worker respects
  its bounds; no process is killed for memory exhaustion or drops pending work.
- Inspect bulk requests: both the document-count and encoded-byte limits hold,
  including action lines. Inject backpressure and an individual document too
  large for a bulk request. Work remains pending with a specific error.
- Add a QA search node and verify shard redistribution without a Tack routing
  change. Confirm sufficient disk for the serving and replacement indexes.
  Production sizing requires measured capacity for the declared workload,
  including guest failure and rebuild, rather than the model's download size.
