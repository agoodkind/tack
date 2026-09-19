# OpenSearch search architecture

TACK-517 defines Tack search. TACK-518, TACK-519, and TACK-520 implement this
architecture. TACK-524 and TACK-525 define the separate node-storage contract.

## Search behavior

People and agents must be able to find a Tack node by its words or meaning,
including information near the end of a large node. Results must belong
to the caller's authorized organization and requested scope. Search must
continue after any one search guest fails.

OpenSearch 3.8 will provide keyword and semantic ranking. FoundationDB will
store the authoritative nodes, properties, relationships, and type metadata.
Search will return distinct node results populated from current FoundationDB
reads. A passage is a bounded portion of a node's searchable text.

A request supplies query text and a metadata-defined entry point, with optional
scope, node type, and continuation cursor. Tack derives the organization from
that entry point and verifies membership. The caller cannot select an arbitrary
organization ID. Scope and type names follow metadata at any hierarchy depth.

Results contain node identity, the current name, and bounded current-node
summaries. Full content remains available through node retrieval. A page holds
at most 25 nodes and respects Tack's response-byte budget. Fewer results are
valid when the byte budget is reached; the cursor must retain the next result.
Search failure is an explicit error, never a successful empty result.

## Searchable content

Node types, property types, and property names are opaque identifiers. Search
must work with definitions created after deployment and without product seeds.
It must not maintain an allowlist, switch on a type identifier, or infer meaning
from an identifier's spelling.

The node metadata declares which values contribute searchable content and how
their stored structure becomes text. Tack's generic metadata interpreter
produces ordered text fragments from the node name and declared values. Search
consumes those fragments without interpreting the underlying types. Projection
rules describe value structure and text representation declaratively; a new
type must not require a new type-specific extractor in application code.

The target metadata contract must express applicability, inclusion or exclusion,
text representation, and deterministic ordering for structured values.
Missing or invalid projection declarations fail
explicitly; an unfamiliar type must never be silently excluded from search.

Every included fragment contains its complete decoded text. Absent values
contribute no text; empty property sets still permit name search. Malformed
values fail indexing with node and property identifiers. Equivalent values and
metadata produce the same ordered fragments regardless of map iteration order.

Tack will reindex affected nodes when projection declarations, display text,
applicability, type metadata, or ancestry change. A moved subtree must acquire
its new search scope. Structured property filtering, property sorting, and
category totals are not part of this search contract.

## Complete passage coverage

Search must cover the entire searchable text of every node Tack accepts. It
must not impose a fixed passage count or combine excess text into an oversized
last passage. TACK-524 provides recoverable size errors while storage remains
limited. TACK-525 introduces chunked storage and proposes an 8 MiB serialized
node limit. Search accepts the storage contract's supported size; it does not
embed a separate 8 MiB ceiling.

The indexer reads a complete, verified node view through Tack's reader. Storage
chunks do not define passage boundaries. Corrupt or incomplete storage records
fail indexing without publishing a replacement generation.

OpenSearch ML Commons will run
`huggingface/sentence-transformers/all-MiniLM-L6-v2` version 1.0.2 in TorchScript
format. It produces 384-dimensional vectors. The upstream model declares a
[256-token sequence limit](https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/blob/main/sentence_bert_config.json).
Deployment pins compatible model and tokenizer artifacts with their checksums.

Tack divides original text spans into passages of at most 192 model tokens
including special tokens and 16 KiB of UTF-8, with a target overlap of 32 tokens.
It preserves Unicode boundaries and re-tokenizes each emitted span to verify that
inference cannot truncate it. Adjacent spans cover every source character;
overlap must not prevent forward progress. Every passage has its own
OpenSearch document. OpenSearch generates its embedding through the
`node-passages-embed` ingest pipeline's `text_embedding` processor.

Query text uses the same tokenizer and model. Empty queries and queries above
192 model tokens return a recoverable validation error with the limit. Tack
must not silently shorten a query. Model mismatch, missing inference capacity,
and embedding failure are explicit failures.

## Passage documents

The `node-passages` alias selects one versioned index. Each backing index has
three primary shards and one replica per primary on a different guest. Each
document has the following fields; arbitrary property names do not create
OpenSearch mapping fields.

| Field | Representation |
| --- | --- |
| `node_id` | Canonical node UUID as a keyword. |
| `org_id` | Authoritative organization UUID as a keyword. |
| `scope_ids` | The node and its authorized ancestor IDs as keywords. |
| `node_type` | The metadata-defined type key as a keyword. |
| `content_generation` | SHA-256 of the canonical projection and indexing configuration. |
| `passage_ordinal` | Zero-based integer position within the generation. |
| `name` | A Unicode-safe prefix of at most 1 KiB for keyword boosting; passages cover the full name. |
| `passage_text` | One complete original text span. |
| `embedding` | A 384-dimensional Lucene HNSW vector using cosine similarity. |

The canonical generation input includes identity, organization, scope, type,
name, projected fragments and metadata, and model, tokenizer, and projection
versions. Equivalent inputs produce identical passage IDs. Each ID combines
node ID, generation, and ordinal. Identical text in different organizations
never shares an index document.

## Ranking and pagination

A hybrid query searches `name` with boost 3 and `passage_text` with ordinary
keyword weight, and searches `embedding` for semantic similarity. The
`node-passages-hybrid` search pipeline uses `min_max` score normalization and
`arithmetic_mean` combination, initially with equal branch weights. QA relevance
measurements determine the release configuration without maintained synonyms.

Every branch applies validated organization, scope, and optional type filters
as structured JSON. All returned candidates, including any exact-reference
lookup, undergo current authorization and scope checks through the node reader.
Unavailable authoritative reads fail the request; deleted or no-longer-visible
nodes are omitted. Candidate text is never used as the returned node body.

OpenSearch's [hybrid collapse](https://docs.opensearch.org/latest/vector-search/ai-search/hybrid-search/collapse/)
groups passage candidates by `node_id`. Candidate depth and nearest-neighbor
counts are tuning parameters, not unique-node guarantees. A node with thousands
of passages must not prevent other eligible nodes from filling a result page.
The implementation must pass the duplicate-heavy acceptance case before its
candidate settings are accepted.

A continuation represents one bounded ranked set of at most 1,000 distinct node
IDs. It binds the query, resolved filters, authenticated principal, and index
version. Its ordering is fixed for that continuation; current authorization is
checked on every page. Expired or mismatched cursors return a recoverable error.
Reaching the ranked-set bound reports that bound rather than claiming exhaustive
results. OpenSearch passage hit counts are never presented as node totals.

The reader fetches candidates in batches bounded by reconstructed bytes and
renders results in rank order. A response limit cannot silently consume an
unreturned node. Candidate refill may require more than one search or storage
request; acceptance measures correctness and resource bounds, not call counts.

## Durable indexing and recovery

A committed node mutation records pending indexing work durably in FoundationDB
as part of the authoritative commit. A failed index write cannot lose that work
or invalidate the committed node. Workers resume pending creates, updates, and
deletions after restart; logs alone are not a retry mechanism. Metadata and
ancestry changes also schedule durable work for affected nodes.

Workers use node revision ordering and fenced ownership so a delayed writer
cannot replace a newer projection or resurrect a deleted node. The content hash
identifies a projection; it does not establish which mutation is newer. New
passages are indexed and refreshed before prior passages are removed. Partial
bulk failure keeps work pending. Temporary mixed generations can affect ranking,
but each result remains one authorized node with current FoundationDB content.

Bulk requests contain at most 500 passage documents and at most 5 MiB of encoded
request data, including action lines. An oversized individual document fails
explicitly. Readers, tokenizer work, inference batches, and concurrent workers
have bounded memory. Backpressure retains work durably instead of dropping it.

Reindexing builds a fresh versioned index from verified FoundationDB reads.
It records a mutation boundary, catches up changes committed during the scan,
and coordinates the final alias switch with index workers. Scan, inference,
catch-up, or validation failure leaves the serving alias unchanged. After
switching, live workers target the new index. Restoring FoundationDB also
rebuilds search; stale search data cannot override restored source records.

## Deployment and capacity

QA and production each use three LXC guests. QA runs on `suburban`; production
runs on `vault`. Each environment has `tack-search1`, `tack-search2`, and
`tack-search3`, each with 4 GB memory, 2 CPU cores, 40 GB hot-tier storage, and
2 GB JVM heap. The container image is `opensearchproject/opensearch:3.8.0`.
This topology tolerates a guest failure, not loss of its single hypervisor.

All three guests store data and are cluster-manager eligible. Inference must
remain available after any one guest stops. ML Commons permits inference on
these data nodes with `plugins.ml_commons.only_run_on_ml_node=false`. The model
artifact is about 90 MB; that is not its runtime memory requirement. JVM, native
inference memory, filesystem cache, and concurrent work must fit the guest limit.

The configs repository provisions LXCs, networking, secrets, certificates, and
environment variables. Tack owns the containers, mappings, pipelines, and Go
client. Deployment follows the repository's Ansible path and IPv6-only network
contract. REST and transport require verified TLS. The application uses scoped
credentials; provisioning credentials are separate. Model download requires
outbound HTTPS during provisioning, not an external inference service.

Capacity depends on total passages, repeated metadata, vectors, replicas, and
rebuild headroom. Measurements must include disk use, peak memory, latency,
indexing throughput, and pending-work age. Adding search nodes must redistribute
shards without application-owned placement. A rebuild needs room for both indexes.
The [acceptance criteria](2026-09-19-search-acceptance.md) define release evidence.
