# OpenSearch search architecture

TACK-517 specifies search. TACK-518 through TACK-520 implement it. TACK-524 and
TACK-525 replace the current node storage limit behind the same paginated reader.

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

Pages collectively contain the complete decoded text of every included value and
name. One value can span any number of pages. Invalid declarations or values fail
with node and property identifiers. Equivalent values and metadata produce the
same ordered text regardless of map iteration order.

Projection, display text, type metadata, and ancestry changes schedule affected
nodes for indexing. Structured property filtering, property sorting, and category
totals are outside this contract.

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

OpenSearch ML Commons runs
`amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte` version 1.0.0.
Deployment pins the 554,924,400-byte TorchScript bundle with SHA-256
`08879b93faf4a92506a44e150f47bbc4cadc9a2f083350c4dc79434738303047` and its
tokenizer with SHA-256
`ea725c60b9022a7a491ffc348b5622a199853c806d625f673d0e2ebf1c3b5312`.
OpenSearch remains unmodified. Custom plugins, forks, and external inference are
excluded.

OpenSearch preserves `page_text` for keyword search. Its ingest pipeline uses a
native `gsub` processor to create 160-character texts at 80-character intervals.
Its native `text_chunking` delimiter processor preserves those inputs and every
existing newline. `max_chunk_limit` is `-1`. Its native `sparse_encoding` processor
stores each result in a nested `rank_features` field. Retired and empty pages do
not invoke the model.

Reader pages contain at most 4,096 UTF-8 bytes. Validation extracts the tokenizer
from the pinned bundle and proves every generated input fits the model's 512-token
limit. Tack enforces only the byte bound. Model or processor changes require the
same coverage proof before release.

## Indexed pages

The `node-pages` alias selects one versioned physical index. Each index generation
records its model identity, pipeline version, primary shard count, and replica
count. Operators can increase primary shards only by building and validating a
replacement index. Adding a node requires no Tack routing change.

The mapping contains only these fixed fields:

| Field | Representation |
| --- | --- |
| `node_id` | Canonical node UUID as a keyword. |
| `org_id` | Authoritative organization UUID as a keyword. |
| `scope_ids` | The node and authorized ancestor IDs as keywords. |
| `node_type` | The metadata-defined type key as a keyword. |
| `node_revision` | The committed source revision. |
| `projection_version` | The metadata, pagination, pipeline, and model version. |
| `page_ordinal` | The stable page position. |
| `name` | A bounded Unicode prefix for lexical boosting. |
| `page_text` | Original text for lexical search. |
| `sparse_text` | Temporary overlapping text with indexing disabled. |
| `sparse_chunks` | OpenSearch-generated bounded model inputs. |
| `sparse_embedding` | Nested sparse token weights in `rank_features`. |
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

Each engine request returns at most 100 page matches through `search_after`. Each
public response reads at most four engine batches. An empty deduplicated response
can still include a continuation. Only an empty raw engine batch ends traversal.
No request assembles every match or every visited ID.

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

## Rebuild and index lifecycle

Only one rebuild can run in an environment. A rebuild scans FoundationDB into one
replacement index, replays mutations after its boundary, verifies the replacement,
then switches the alias atomically. At most one serving, one replacement, and one
retiring index can exist. A second rebuild waits until retirement finishes.

Each environment sets maximum retired-page count, oldest-retirement age, and
physical-index bytes from its validated disk budget. Crossing any threshold starts
a replacement-index rebuild. The disk budget reserves space for the serving,
replacement, and retiring indexes. When that reserve is unavailable, workers leave
new index work pending instead of consuming it, while source writes remain durable.

New sessions cannot use a retiring index. Existing sessions end at their inactivity
or absolute deadline. Bounded cleanup deletes session state and then deletes the
retiring index. Restore operations always create a new search generation and index.

## Deployment and capacity

QA and production each use three LXC guests. QA runs on `suburban`; production runs
on `vault`. Each guest starts with at least 8 GiB of memory, 2 CPU cores, 40 GiB of
hot-tier storage, and a 2 GiB JVM heap. The image is
`opensearchproject/opensearch:3.8.0`. Every guest stores data, can manage the
cluster, and runs local inference. One guest can stop without losing a primary or
the model.

The full model reports about 666 MB of inference memory. A local container used
about 3.2 GiB after deployment and validation. The same test opened the ML memory
circuit breaker at a 4 GiB container limit. Eight GiB is the validation floor, not
a production capacity claim.

Release capacity uses a declared workload and pass thresholds for query latency,
index throughput, pending-work age, memory, disk, one-guest failure, and concurrent
rebuild. A scale-out test adds a node and rebuilds with a higher primary-shard count.
It must increase measured throughput without application routing changes. Disk must
fit serving, replacement, and retiring indexes during handoff.
