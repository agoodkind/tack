# OpenSearch search architecture

TACK-517 specifies search. TACK-518 through TACK-520 retain cross-cutting
acceptance. TACK-530 through TACK-542 implement it. TACK-524 and TACK-525 replace
the current node storage limit behind the same paginated reader.

Meilisearch is absent from the target architecture. The implementation deletes
its client, configuration, adapters, test environment, container, volume,
credentials, and operational documentation. It does not migrate the Meilisearch
index, write to both engines, preserve a fallback, or retain a compatibility
layer. Provisioning creates an empty OpenSearch index and rebuilds it only from
FoundationDB.

## Search behavior

Search finds declared text throughout each node. FoundationDB remains authoritative
for nodes, metadata, relationships, and authorization. OpenSearch 3.8 indexes and
ranks bounded text pages. Each text page becomes one OpenSearch document.

A request supplies query text and a metadata-defined entry point. Tack resolves
the organization and verifies membership. Optional scope and node type filters use
opaque metadata identifiers. The caller cannot select an arbitrary organization.

Each response contains at most 25 distinct node IDs with bounded current summaries.
The node reader supplies those summaries and current authorization. Search never
returns indexed page text as the node body. A continuation can reach every matching
node. Engine, inference, source, and session failures return explicit errors.

## Searchable content

Node types, property types, and property names are opaque identifiers. Metadata
defines applicability, inclusion, text representation, and order. Tack uses one
generic interpreter. Application code contains no product type allowlist and no
property-specific extraction switch.

Every applicable property definition explicitly includes or excludes search. A
missing declaration is invalid. The FoundationDB `Indexed` flag cannot supply a
default because it controls secondary lookup keys rather than searchable text.
Seeds for new organizations and QA data declare search behavior for convenience,
but runtime behavior depends only on stored metadata.

Pages collectively contain the complete decoded text of every included value and
name. One value can span any number of pages. Invalid declarations or values fail
with node and property identifiers. Equivalent values and metadata produce the
same ordered text regardless of map iteration order.

Projection, display text, type metadata, and ancestry changes schedule affected
nodes for indexing. Structured property filtering, property sorting, and category
totals are outside this contract.

Before the first OpenSearch rebuild, an audited one-time command applies a
complete manifest reviewed by an operator to existing definitions. It never
infers behavior from an identifier or type and never overwrites an existing
declaration. Public search remains unavailable until no definition lacks an
explicit decision.

## Paginated node reads

The reader returns one bounded Unicode text page per call. Each response includes
the committed node revision, projection version, stable ordinal, next cursor, and
an explicit completion value. The worker never infers completion from page size,
current storage limits, or prior reads.

Each read uses bounded memory. The storage adapter owns FoundationDB keys and record
layout. The current adapter can return one page for ordinary nodes. Another adapter
can start returning successive pages at any time without changing worker, index,
query, retry, or grouping behavior.

Every continuation reads the same source revision and projection. A changed,
missing, or corrupt revision returns an error. Search starts indexing before the
reader reaches the final page. Tack does not combine all pages, count model tokens,
or split text for the model.

## Native sparse semantic indexing

OpenSearch ML Commons runs `amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte` version 1.0.0. Deployment pins its 554,924,400-byte TorchScript bundle with SHA-256 `08879b93faf4a92506a44e150f47bbc4cadc9a2f083350c4dc79434738303047` and records the bundled tokenizer SHA-256 `ea725c60b9022a7a491ffc348b5622a199853c806d625f673d0e2ebf1c3b5312`. Tack does not run or reproduce the tokenizer. OpenSearch remains unmodified. Custom plugins, forks, external inference, and application-defined ingest pipelines are excluded.

The native `semantic` field preserves `page_text` for lexical search. Its fixed-character chunking uses a 160-character limit, 0.5 overlap, and unlimited chunk count. Native sparse encoding applies `max_ratio` pruning at 0.1 and stores each embedding in the generated nested `page_text_semantic_info.chunks.embedding` `rank_features` field.

Reader pages contain at most 4,096 UTF-8 bytes. Validation inspects the source, every generated chunk, every sparse embedding, and the final character through OpenSearch. Tack enforces only the byte bound and does not reproduce the model tokenizer. Model or semantic field changes require the same coverage proof before release.

Tack pins `github.com/opensearch-project/opensearch-go/v4` v4.7.3, the latest stable client. A compatibility test runs every used core API against the exact OpenSearch 3.8.0 image because the client documents later 3.x releases as best effort. Typed APIs implement core operations and construct search requests. A narrow search response decoder preserves replacement point-in-time IDs and exact sort JSON that `SearchResp` omits or converts. Concrete ML Commons request types satisfy `opensearch.Request`; `opensearch.Do` decodes responses and `opensearch.ParseError` decodes failures. Stable v4.7.3 lacks ML Commons APIs. Tack exposes no generic method-and-path JSON transport and does not import the temporary v5 preview package.

## Indexed pages

The `node-pages` alias selects one versioned physical index. Each generation records its model, mapping version, primary shards, reserved routing shards, and replicas.
When only the primary count changes, OpenSearch splits the serving index into a validated replacement along its reserved routing path.
Model, mapping, restore, cleanup, and unsupported shard changes rebuild from FoundationDB. Adding a node requires no Tack routing change.

The mapping contains only these fixed fields:

| Field | Representation |
| --- | --- |
| `node_id` | Canonical node UUID as a keyword. |
| `org_id` | Authoritative organization UUID as a keyword. |
| `scope_ids` | The node and authorized ancestor IDs as keywords. |
| `node_type` | The metadata-defined type key as a keyword. |
| `node_revision` | The committed source revision. |
| `projection_version` | The metadata, pagination, semantic mapping, and model version. |
| `page_ordinal` | The stable page position. |
| `name` | A bounded Unicode prefix for lexical boosting. |
| `page_text` | Native `semantic` field with original text for lexical search. |
| `page_text_semantic_info` | OpenSearch-generated nested chunks and sparse `rank_features` embeddings. |
| `retired` | A Boolean that excludes obsolete records. |

Document IDs combine organization, node, revision, projection version, and page
ordinal. Retrying one page overwrites the same document.

## Ranking and continuation

OpenSearch generates sparse query weights once with the pinned model. Tack stores
the returned token-weight JSON as opaque bounded session data. Tack does not load
the tokenizer or interpret token keys. Later engine requests reuse `query_tokens`
and do not repeat inference.

The query adds lexical scores from `name` with boost 3 and `page_text` to the
nested sparse semantic score. Each page uses its greatest nested score. OpenSearch
executes conventional sparse search over its inverted index. The query uses no
dense script, nearest-neighbor `k`, hybrid result window, field collapse, or fixed
total-result limit.

Organization, scope, optional type, and retirement filters use structured JSON.
OpenSearch sorts page matches by descending score, ascending node ID, then
`_shard_doc`. A point in time freezes index contents and makes `_shard_doc` a stable
page-level tie breaker. The first page match for a node establishes that node's
rank. Tack skips later matches for visited nodes.

The official client receives all three addresses and owns TLS, connection pooling, routing, retries, failed-node recovery, and transport metrics. Each engine response returns at most 100 page matches through `search_after`. OpenSearch distributes shard work across the three containers. Each public response reads at most four engine batches. An empty deduplicated response can still include a continuation. Only an empty raw engine batch ends traversal. No request assembles all matches or visited IDs.

The session binds the normalized query, filters, principal, physical index, and
search generation. Current authorization applies before each node is returned.
Committed progress renews a 15-minute inactivity deadline. Every session also has
a two-hour absolute deadline, so an old physical index cannot remain indefinitely.
Expired or mismatched cursors require a new search.

## Durable indexing and bounded work

Every source mutation atomically records desired search work in FoundationDB.
Workers persist revision, projection, phase, and reader cursor. A worker invocation
processes at most 32 pages or 5 MiB of encoded requests. It starts no new remote
operation after its two-second slice deadline. One remote operation can remain in
flight until its ten-second timeout, then the worker saves progress and yields.
Cleanup processes at most 100 document IDs before yielding. A crash repeats only
the uncommitted slice.

Live mutations, cleanup, metadata rescans, and rebuild work use separate bounded
queues or worker limits. A large node, rebuild, or cleanup cannot monopolize all
workers. Backpressure leaves durable work pending.

After new pages are refreshed, workers retire every older page through bounded
resumable operations. A delayed writer cannot restore retired text. Converged FDB
state contains one desired generation per node plus current issued IDs. Completion
deletes obsolete work, cursor, error, and issued-ID records. Rebuild journals are
removed after no active rebuild needs them. State does not grow with mutation
history.

## Index replacement lifecycle

Only one index replacement can run in an environment. A full replacement scans FoundationDB. A primary-shard-only replacement blocks engine writes briefly and uses OpenSearch's native split operation.
FoundationDB writes still commit and record durable search work. Both paths replay pending mutations, verify the target, switch the alias atomically, and use the same retirement state.
At most one serving, one replacement, and one retiring index can exist. Another replacement waits.

Each environment sets maximum retired-page count, oldest-retirement age, and
physical-index bytes from its validated disk budget. Crossing any threshold starts
a full FoundationDB replacement. The disk budget reserves space for the serving,
replacement, and retiring indexes. When that reserve is unavailable, workers leave
new index work pending instead of consuming it, while source writes remain durable.

New sessions cannot use a retiring index. Existing sessions end at their inactivity
or absolute deadline. Bounded cleanup deletes session state and then deletes the
retiring index. Restore operations always create a new search generation and index.

## Deployment and capacity

Tack request handlers keep no process-local search state. Every instance opens or
continues sessions through FoundationDB. Stable hash buckets distribute session and
work keys without sticky routing or a global claim range.

QA and production start with three combined-role LXC guests on `suburban` and
`vault`, respectively. Each guest uses OpenSearch 3.8.0 with at least 8 GiB memory,
2 CPU cores, 40 GiB storage, and a 2 GiB JVM heap. One guest can stop without losing
a primary or the model.

OpenSearch dispatches ML work across eligible ML nodes and routes search across primary
and replica shards. Model deployment specifies no node IDs and includes new ML
nodes. ML-only nodes increase inference capacity. Data nodes and replicas increase
ranking capacity. Native index splitting increases primary shards without regenerating
existing embeddings when the reserved routing path permits it. Tack selects neither
ML workers nor shard nodes.

The final GTE sparse workload opened the ML memory circuit breaker at 4 GiB. It
completed at 8 GiB and used about 3.4 GiB afterward. Eight GiB is the QA floor and
production starting allocation, not a production capacity result.

Release capacity sets pass thresholds for latency, throughput, pending-work age,
memory, disk, one-guest failure, and concurrent replacement. Separate tests add Tack,
FoundationDB, ML, and data capacity. Each addition must improve the relevant fixed
workload without application code changes. Disk must fit all three index generations.
