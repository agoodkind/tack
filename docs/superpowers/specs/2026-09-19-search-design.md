# OpenSearch search architecture

TACK-517 specifies search; TACK-518, TACK-519, and TACK-520 implement it.
Search uses a paginated read interface from the start. TACK-524 and TACK-525 address storage.

## Search behavior

Search must find text throughout each node and enforce current authorization
and scope. Search must continue after any one search guest fails.

OpenSearch 3.8 will provide keyword and semantic ranking. FoundationDB will
store the authoritative nodes, properties, relationships, and type metadata.
Each returned text page becomes a separate OpenSearch document. Search returns
distinct nodes with current FoundationDB content without assembling multiple pages.

A request supplies query text and a metadata-defined entry point, with optional
scope, node type, and continuation cursor. Tack derives the organization from
that entry point and verifies membership. The caller cannot select an arbitrary
organization ID. Scope and type names follow metadata at any hierarchy depth.

Results contain node identity and bounded current names and summaries. Full
content remains available through paginated node retrieval. A result page has
at most 25 nodes within Tack's response-byte budget. Its cursor retains any
unreturned node. Search failure is an explicit error, never a successful empty result.

## Searchable content

Node types, property types, and property names are opaque identifiers. New
definitions must work after deployment without product seeds, type allowlists,
application switches, or meaning inferred from an identifier's spelling.

Metadata controls applicability, inclusion, text representation, and ordering.
Tack's generic metadata interpreter returns text pages for names and declared
values. Search does not interpret types or use type-specific extractors.
Missing or invalid declarations fail explicitly, including for unfamiliar types.

Pages collectively contain the complete decoded text of every included value.
A single value or name may span any number of pages. Absent values contribute
no text; empty property sets still permit name search. Malformed values fail
indexing with node and property identifiers. Equivalent values and metadata
produce the same ordered text regardless of map iteration order.

Tack will reindex affected nodes when projection declarations, display text,
applicability, type metadata, or ancestry change. A moved subtree must acquire
its new search scope. Structured property filtering, property sorting, and
category totals are not part of this search contract.

## Paginated node reads

Search must cover all accepted text without a fixed total size or page count.
The reader returns one bounded part per call. The worker indexes each part and
continues until an explicit end marker. It cannot infer completion from part
size, current storage limits, or previous reads. Later FDB pagination requires no change
to the search loop, index mapping, page IDs, retries, or result grouping.

Each read and indexing request has a byte limit. The storage abstraction owns
pagination and hides FoundationDB keys, value limits, and record layout.
The initial adapter may read the existing bounded record without changing its storage model.
The future adapter must decode and paginate larger records with bounded memory.

The reader returns bounded Unicode text pages for one committed node revision,
with stable ordinals, a resumable cursor, and an explicit end marker. Metadata
and boundaries remain fixed for that read. Resuming another revision is an error.
Corrupt or missing data is an error, never an end marker.

The worker submits each returned page without model-specific splitting. The
reader's pagination contract must preserve text at page boundaries, including
bounded adjacent context needed by search. It must not depend on a tokenizer.
Search must not collect, hash, or validate all pages before indexing the first.
Multi-page behavior must pass acceptance before the first search release.

## OpenSearch text splitting and embeddings

OpenSearch ML Commons will run the TorchScript model
`huggingface/sentence-transformers/all-MiniLM-L6-v2` version 1.0.2, which produces
384-dimensional vectors. Deployment pins the bundle and included tokenizer by
checksum. Tack neither loads a tokenizer nor counts model tokens. OpenSearch
remains unmodified. Custom plugins, forks, and external inference are excluded.

The page text uses OpenSearch's [semantic field](https://docs.opensearch.org/latest/mappings/supported-field-types/semantic/)
with local inference and built-in text chunking enabled. OpenSearch divides
each page into smaller texts and stores their embeddings within that page's
document. The native chunker uses `max_chunk_limit: -1`; its overflow behavior
must not merge remaining text into an oversized final chunk.
The page byte limit must also bound nested objects and inference memory.

OpenSearch's splitter and model can count tokens differently, as reported in
the [model-based tokenizer proposal](https://github.com/opensearch-project/neural-search/issues/794).
Pagination does not resolve this mismatch. Complete embedding coverage requires
proof before release. Copying tokenizer logic into Tack is not an accepted fix.
Query text must also be processed in full or rejected without a Tack token counter.

## Indexed pages

The `node-pages` alias selects one versioned index with three primary shards
and one replica per primary on another guest. These fields are fixed; arbitrary
property names do not create OpenSearch mapping fields.

| Field | Representation |
| --- | --- |
| `node_id` | Canonical node UUID as a keyword. |
| `org_id` | Authoritative organization UUID as a keyword. |
| `scope_ids` | The node and its authorized ancestor IDs as keywords. |
| `node_type` | The metadata-defined type key as a keyword. |
| `node_revision` | The committed source revision returned by the reader. |
| `projection_version` | The pinned metadata, scope, pagination, and model configuration version. |
| `page_ordinal` | The page's stable position within that revision and projection. |
| `name` | A Unicode-safe prefix of at most 1 KiB for keyword boosting; pages cover the full name. |
| `page_text` | A semantic field with raw text and OpenSearch-generated nested embeddings. |

Document IDs combine organization, node ID, revision, projection version, and
page ordinal. Retrying a page overwrites that document. Metadata or scope changes
create a new projection version even with unchanged text. Nested embeddings use
384 dimensions, Lucene HNSW, and cosine similarity.

## Ranking and pagination

A hybrid query searches `name` with boost 3 and `page_text` by keyword and neural
similarity. The `node-pages-hybrid` pipeline uses `min_max` normalization and
`arithmetic_mean` combination, initially with equal weights. QA relevance tests
determine the release configuration without maintained synonyms.

Every branch applies validated organization, scope, and optional type filters
as structured JSON. All returned candidates, including any exact-reference
lookup, undergo current authorization and scope checks through the node reader.
Unavailable authoritative reads fail the request; deleted or no-longer-visible
nodes are omitted. Candidate text is never used as the returned node body.

OpenSearch's [hybrid collapse](https://docs.opensearch.org/latest/vector-search/ai-search/hybrid-search/collapse/)
groups pages by `node_id`. Validate candidate depth and nearest-neighbor counts:
thousands of pages from one node must not crowd other nodes out of a result page.

A continuation represents one bounded ranked set of at most 1,000 distinct node
IDs. It binds the query, resolved filters, authenticated principal, and index
version. Its ordering is fixed for that continuation; current authorization is
checked on every page. Expired or mismatched cursors return a recoverable error.
Reaching the ranked-set bound reports that bound rather than claiming exhaustive
results. OpenSearch page hit counts are never presented as node totals.

The reader fetches current authorization data and bounded summaries without
collecting multiple content pages. Results follow rank order. Candidate refill may
require several search or storage requests; each request must remain bounded.

## Durable indexing and recovery

A node mutation atomically commits pending indexing work in FoundationDB.
Failed indexing cannot lose that work or invalidate the committed node. Workers
resume creates, updates, and deletions after restart. Metadata and ancestry
changes also schedule durable work for affected nodes.

Workers persist the source revision, projection version, and completed cursor.
Progress advances only past successfully indexed pages. A crash before saving
progress safely repeats those page IDs; partial bulk failure cannot skip a page.
The end marker and successful writes establish completion. A lost source revision
requires an explicit restart against a current revision, not a mixed read.

Workers enforce revision ordering and exclusive write ownership. A delayed
writer cannot replace a newer projection or resurrect a deleted node. After
all new pages are indexed and refreshed, workers remove older pages in bounded,
resumable operations. This also removes excess pages when an edit shortens a
node. Deletion removes every page. Temporary mixed revisions may affect ranking;
each result remains one authorized node with current FoundationDB content.

Bulk requests contain at most 500 page documents and 5 MiB of encoded data,
including action lines. Page sizing reserves room for metadata and encoding.
Search memory depends on page size and concurrency, not total node size.
Backpressure retains pending work; cleanup and inference also remain bounded.

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
environment variables. Tack owns containers, mappings, pipelines, and the client.
Deployment uses Ansible and IPv6-only networking. REST and transport verify TLS.
Application credentials are scoped and separate from provisioning credentials.
Model provisioning requires outbound HTTPS; ordinary inference remains local.

Measure disk use, peak memory, latency, indexing throughput, and pending-work age
with pages, nested embeddings, metadata, and replicas included. A rebuild needs
room for both indexes. Added search nodes must redistribute shards without Tack
placement logic. Release requires the [acceptance criteria](2026-09-19-search-acceptance.md).
