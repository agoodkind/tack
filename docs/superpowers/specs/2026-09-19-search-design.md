# OpenSearch search architecture

TACK-517 defines the complete search replacement. TACK-518 through TACK-520 define its scalability, isolation, and relevance acceptance. TACK-530 through TACK-544 implement and release it. TACK-524 and TACK-525 change node storage without changing the paginated reader contract.

## Problem

The current search path uses one Meilisearch 1.12 container with a 2 GiB memory limit. Production returned no results for ordinary and exact-title queries on September 18, 2026. The endpoint has no accepted result behavior that the replacement must preserve. Migrating or preserving Meilisearch state adds work without protecting source data. FoundationDB already stores the authoritative nodes and can rebuild the index. Meilisearch cannot enforce the tenant boundary. Every organization shares one master-key connection, and the adapter formats filters as strings. The MCP tool must read each result from FoundationDB and apply current organization and scope checks. Search stops when the container or its guest stops, and the deployed index has no copy on another guest. Meilisearch matches words rather than meaning. Queries such as `db` require maintained synonyms to find `Database failover`.

## Decision

Tack replaces Meilisearch with OpenSearch 3.8. QA and production each start with one node and no replica. QA uses OpenSearch's single-node discovery mode. Production forms a normal cluster with one initial member so later nodes can join without changing Tack or rebuilding embeddings. OpenSearch generates local sparse semantic embeddings through its native `semantic` field and ranks any number of bounded reader pages. FoundationDB remains authoritative for nodes, metadata, relationships, authorization, durable indexing work, search sessions, and rebuild coordination. Tack does not select shard nodes, count model tokens, or implement a tokenizer.

Removal and replacement ship as separate releases. The first release deletes the Meilisearch client, adapters, dependency, configuration, startup setup, indexing hooks, batch reindex operation, Meilisearch test environment, deployed service, credentials, and operational documentation. The `tack_search` tool remains registered, but every call returns exactly `Search is temporarily unavailable.` This response also applies to exact node references. Every other MCP tool and every FoundationDB or SQL source write continues to operate. The old Meilisearch volume remains untouched until its deletion receives separate authorization.

A later release provisions an empty OpenSearch index, rebuilds it only from FoundationDB, and replaces the temporary unavailable response after acceptance passes. The rebuild includes source mutations committed during the temporary outage. It does not migrate the Meilisearch index, write to both engines, preserve a fallback, or retain a compatibility layer. No Meilisearch document, schema, setting, synonym, ranking rule, result, volume, or code becomes an OpenSearch input.

## Search behavior

Search finds declared text throughout each node. FoundationDB remains authoritative for nodes, metadata, relationships, and authorization. OpenSearch 3.8 indexes and ranks bounded text pages. Each text page becomes one OpenSearch document.

A request includes query text and a metadata-defined entry point. Tack resolves the organization and verifies membership. Optional scope and node type filters use opaque metadata identifiers. The caller cannot select an arbitrary organization.

Each response contains at most 25 distinct node IDs with bounded current summaries. The node reader loads each OpenSearch batch through one bounded FoundationDB operation and applies current authorization. Search never returns indexed page text as the node body. A continuation can return every matching node. Engine, inference, source, and session failures return explicit errors.

The permission boundary compiles current FoundationDB state into opaque access keys for indexed nodes and callers. The current policy derives them from organization and scope; a future policy can derive the same shape from permission nodes and relationships without changing search storage or queries. OpenSearch rejects most forbidden candidates before ranking. FoundationDB still checks current authorization before Tack returns a node.

## Searchable content

Node types, property types, and property names are opaque identifiers. Metadata defines applicability, inclusion, text representation, and order. Tack uses one generic interpreter. Application code contains no product type allowlist and no property-specific extraction switch.

Every applicable property definition explicitly includes or excludes search. A missing declaration is invalid. The FoundationDB `Indexed` flag cannot supply a default because it controls secondary lookup keys rather than searchable text.
Seeds for new organizations and QA data declare search behavior for convenience, but runtime behavior depends only on stored metadata.

Pages collectively contain the complete decoded text of every included value and name. One value can span any number of pages.
Invalid declarations or values fail with node and property identifiers. Equivalent values and metadata produce the same ordered text regardless of map iteration order.

Projection, display text, type metadata, and ancestry changes schedule affected nodes for indexing against the serving physical index. Workers reread and reembed only affected pages, then retire obsolete document IDs. These changes do not create a physical index or switch an alias. Structured property filtering, property sorting, and category totals are outside this contract.

Before the first OpenSearch rebuild, an audited one-time command applies a complete operator-reviewed manifest to existing definitions. It never infers behavior or overwrites a declaration. Public search remains unavailable until every definition has an explicit decision.

## Permission projection

OpenSearch stores a strict `access` object with only `versions`, `keys`, and `generation`. Each key combines an opaque policy version with an opaque grant value. Search code does not inspect permission types, roles, groups, organizations, or scopes. The policy reads authoritative nodes and relationships through the node reader and returns sorted, unique, bounded keys.

A future permission definition is itself a node. For example, a definition can interpret one relationship between a principal group node and a resource root node as a read grant. The policy reads that definition and its related nodes, then emits the same opaque key for the permitted resource pages and caller. Search compares keys and never interprets the node or relationship types.

The current policy compiles each permitted organization and scope pair into one opaque key. A future policy can combine several permission facts into one key before returning it. The query policy compiles the caller and selected entry point with the same version. OpenSearch requires the active version and at least one matching key before scoring.

A principal membership change changes caller keys and schedules no document work. A resource grant, ancestry, or other visibility change schedules access-only work for the affected node or a bounded FoundationDB scan. Native bulk updates replace `search_generation` and `access` on every current page without submitting `page_text`. `skip_existing_embedding` must preserve chunks and sparse weights without invoking the model.

FoundationDB assigns one increasing search generation to every content or access change and stores that generation's policy versions with the durable work. Full page writes, access-only updates, and retirement use the generation as the external OpenSearch version. OpenSearch rejects older content and access operations. Retrying the current generation reuses the same policy versions and is idempotent.

A policy change creates a candidate for one authoritative permission root while its active version serves queries. Independent roots can transition concurrently. New writes include both versions. A bounded access scan adds candidate keys to existing pages. Exact verification precedes activation. Existing sessions retain their version; another access scan removes old keys after those sessions finish. Failure before activation preserves the active version. The transition creates no index, switches no alias, reads no page text, and regenerates no embedding.

## Paginated node reads

The reader returns one bounded Unicode text page per call. Each response includes the committed node revision, projection version, stable ordinal, next cursor, and an explicit completion value. The worker never infers completion from page size, storage limits, or prior reads.

Each read uses bounded memory. The storage adapter owns FoundationDB keys and record layout. The current adapter can return one page for ordinary nodes. Another adapter can return successive pages without changing worker, index, query, retry, or grouping behavior.

Every continuation reads the same source revision and projection. A changed, missing, or corrupt revision returns an error. Search indexes before the final page. Tack does not combine all pages, count model tokens, or split text for the model.

## Native sparse semantic indexing

OpenSearch ML Commons runs `amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte` version 1.0.0. Deployment pins its 554,924,400-byte TorchScript bundle with SHA-256 `08879b93faf4a92506a44e150f47bbc4cadc9a2f083350c4dc79434738303047` and records the bundled tokenizer SHA-256 `ea725c60b9022a7a491ffc348b5622a199853c806d625f673d0e2ebf1c3b5312`. Tack does not run or reproduce the tokenizer. OpenSearch remains unmodified. Custom plugins, forks, external inference, and application-defined ingest pipelines are excluded.

The native `semantic` field preserves `page_text` for lexical search. Its fixed-character chunking uses a 160-character limit, 0.5 overlap, and unlimited chunk count. Native sparse encoding applies `max_ratio` pruning at 0.1 and stores each embedding in the generated nested `page_text_semantic_info.chunks.embedding` `rank_features` field.

Reader pages contain at most 4,096 UTF-8 bytes. Validation inspects the source, every generated chunk, every sparse embedding, and the final character through OpenSearch. Tack enforces only the byte bound and does not reproduce the model tokenizer. Model or semantic field changes require the same coverage proof before release.

Tack pins `github.com/opensearch-project/opensearch-go/v4` v4.7.3, the latest stable client. A compatibility test runs every used core API against the exact OpenSearch 3.8.0 image because the client documents later 3.x releases as best effort. Typed APIs implement core operations and construct search requests. A narrow search response decoder preserves replacement point-in-time IDs and exact sort JSON that `SearchResp` omits or converts. Concrete ML Commons request types satisfy `opensearch.Request`; `opensearch.Do` decodes responses and `opensearch.ParseError` decodes failures. Stable v4.7.3 lacks ML Commons APIs. Tack exposes no generic method-and-path JSON transport and does not import the temporary v5 preview package.

## Indexed pages

The `node-pages` alias selects one versioned physical index. Each generation records its model, mapping version, primary shards, reserved routing shards, and replicas.
When only the primary count changes, OpenSearch splits the serving index into a validated replacement along its reserved routing path.
Model, tokenizer, embedding format, mapping, page identity, restore, cleanup, and unsupported shard changes rebuild from FoundationDB. Adding a node requires no Tack routing change.

The mapping contains only these fixed fields:

| Field | Representation |
| --- | --- |
| `node_id` | Canonical node UUID as a keyword. |
| `access.versions` | Opaque permission-policy versions as keywords inside the strict access object. |
| `access.keys` | Opaque versioned grant values as keywords inside the strict access object. |
| `access.generation` | The monotonic FoundationDB search generation used for stale-write rejection. |
| `node_type` | The metadata-defined type key as a keyword. |
| `node_revision` | The committed source revision. |
| `projection_version` | The text metadata, pagination, semantic mapping, and model version. It excludes permission-policy versions. |
| `page_ordinal` | The stable page position. |
| `name` | A bounded Unicode prefix for lexical boosting. |
| `page_text` | Native `semantic` field with original text for lexical search. |
| `page_text_semantic_info` | OpenSearch-generated nested chunks and sparse `rank_features` embeddings. |
| `retired` | A Boolean that excludes obsolete records. |

Document IDs combine organization, node, revision, text projection version, and page ordinal. Permission changes preserve document IDs. Retrying one generation updates the same document.

## Ranking and continuation

OpenSearch generates sparse query weights once with the pinned model. Tack stores the returned token-weight JSON as opaque bounded session data. Tack does not load the tokenizer or interpret token keys. Later requests reuse `query_tokens` without repeating inference.

The query adds lexical scores from `name` with boost 3 and `page_text` to the nested sparse semantic score. Each page uses its greatest nested score. OpenSearch uses its sparse inverted index. The query uses no dense script, nearest-neighbor `k`, hybrid result window, field collapse, or fixed total-result limit.

The permission boundary returns one active version and bounded opaque keys. The query requires that version and at least one caller key. Optional type and retirement filters use structured JSON.
OpenSearch sorts page matches by descending score, ascending node ID, then
`_shard_doc`. A point in time freezes index contents and makes `_shard_doc` a stable
page-level tie breaker. The first page match for a node establishes that node's
rank. Tack skips later matches for visited nodes.

The official client receives one environment endpoint and owns TLS, connection
pooling, retries, and transport metrics. A health-checking proxy on that
environment's hypervisor selects an OpenSearch node. Each environment starts with
one backend. The selected node coordinates shard work. Each engine response returns at most 100
page matches through `search_after`. Each public response reads at most four
engine batches. An empty deduplicated response can still include a continuation.
Only an empty raw engine batch ends traversal. No request assembles all matches
or visited IDs.

The session binds the normalized query, filters, principal, physical index, and
search generation. Current authorization applies before each node is returned.
Committed progress renews a 15-minute inactivity deadline. A two-hour absolute
deadline limits how long a session can keep an old physical index.
Expired or mismatched cursors require a new search.

## Durable indexing and bounded work

Every content or resource-access mutation atomically records desired search work
in FoundationDB. Principal membership changes affect query keys and record no
document work. Workers persist generation, work kind, revision, projection,
phase, and reader cursor. A worker invocation
processes at most 32 pages or 5 MiB of encoded requests. It starts no new remote
operation after its two-second slice deadline. One remote operation can remain in
flight until its ten-second timeout, then the worker saves progress and yields.
Cleanup processes at most 100 document IDs before yielding. A crash repeats only
the uncommitted slice.

Content mutations, access updates, cleanup, metadata rescans, and rebuild work
use separate bounded queues or worker limits. Access-only work reads bounded
issued document IDs and updates only `search_generation` and the strict `access` object. A large node,
access backfill, rebuild, or cleanup cannot monopolize all workers. Backpressure
leaves durable work pending.

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

Permission-policy versions do not start index replacement. Their access-only transition preserves page text and sparse weights. A projection change increments the organization epoch and schedules bounded content work against the serving index without starting index replacement. Workers reread and reembed only affected pages and retire their obsolete document IDs. Physical mapping, semantic model, tokenizer, embedding format, page identity, restore state, cleanup state, and unsupported shard changes require replacement.

## Deployment and capacity

Tack request handlers keep no process-local search state. Every instance opens or continues sessions through FoundationDB. Stable hash buckets distribute session and work keys without sticky routing or a global claim range.

QA starts with one combined-role LXC guest on `suburban`. Production starts with one
on `vault`. Each guest uses OpenSearch 3.8.0 with at least 8 GiB memory, 2 CPU cores,
40 GiB storage, and a 2 GiB JVM heap. Both environments use zero replicas and become
unavailable when their search guest stops. Production claims node failover only after
at least three members and one replica pass the production failure test.

Each hypervisor exposes one stable HTTPS search endpoint on its guest-segment
address. Its proxy verifies backend certificates, checks readiness, and selects
the configured backends. Tack never stores cluster membership. Production uses
normal discovery from its first start. New members discover the existing cluster,
then the proxy adds their addresses. Adding ML-only or data-only nodes changes only
OpenSearch membership and proxy configuration. Dedicated coordinating nodes can
later replace the proxy's backend pool without changing Tack. The endpoint adds no
new host failure domain because every search guest in an environment already
depends on that hypervisor.

OpenSearch dispatches ML work across eligible ML nodes and routes search across primary
and replica shards. Model deployment specifies no node IDs and includes new ML
nodes. ML-only nodes increase inference capacity. Data nodes and replicas increase
ranking capacity. Native index splitting increases primary shards without regenerating
existing embeddings when the reserved routing path permits it. Tack selects neither
ML workers nor shard nodes.

The final GTE sparse workload opened the ML memory circuit breaker at 4 GiB. It completed at 8 GiB and used about 3.4 GiB afterward. Eight GiB is the per-guest floor, not a capacity result. Suburban can provision one capped 8 GiB QA guest, but the permanent workload must keep at least 6.26 GiB of host memory available. QA activation requires a complete indexing, query, and rebuild workload before the guest remains enabled. The earlier 3.4 GiB post-workload reading plus the proxy would leave about 6.69 GiB, but that reading did not measure peak use. One guest passes the host CPU and fast-storage projections.

QA and production capacity set pass thresholds for latency, throughput, pending-work age, memory, disk, recovery, and concurrent replacement. The initial release makes no
OpenSearch failover claim. Before production adds nodes, verify cluster joining, model
placement, proxy selection, replica creation, shard placement, and one-member failure.
Disk must fit all three index generations.
