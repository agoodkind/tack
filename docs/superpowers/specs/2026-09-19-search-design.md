# Search on OpenSearch

Epic TACK-517. Resolves TACK-518, TACK-519, TACK-520.

## Problem

Search runs in one Meilisearch 1.12 container on the app guest with a 2 GB
limit. Three things are wrong with it.

1. It cannot grow. Community Meilisearch has no sharding or replication;
   sharding is an Enterprise Edition feature (1.37 and later) whose license
   forbids production use without a commercial agreement. Meilisearch itself
   has no comparable open path, and routing orgs across several community
   instances from the app was rejected because the app would own the shards.
2. It is not tenant safe at the engine. The org filter is built with
   `fmt.Sprintf` and includes the caller's `node_type` argument, and one
   master key serves every org. Nothing leaks today only because the tool
   re-reads every hit from FoundationDB and drops other orgs.
3. It matches letters. "db" does not find "Database failover". The synonym
   feature that tried to paper over this (TACK-510) was reverted.

Production holds 3,842 indexed nodes today (reindex on 2026-09-19), so the
current engine is not slow. The design below is for the product this is
meant to be, not for the current row count.

## Decision

Replace Meilisearch with an OpenSearch 3.8 cluster: three nodes on three new
guests, one index, hybrid keyword and vector ranking with a model the cluster
runs on its own CPUs. OpenSearch is Apache 2.0, shards and replicates without
a license, and its ml-commons plugin serves sentence embedding models in
process, so no node text leaves the guests and queries cost nothing.

## Cluster

Three LXC guests per environment, `tack-search1` to `tack-search3`, on the
suburban hypervisor for QA and vault for production, 4 GB memory and 2 cores
each, roots on the hot tier because a query is a few disk reads on the
request path. Vault had 64 GB free on 2026-09-19. Each guest runs one
`opensearchproject/opensearch:3.8.0` container from the tack compose file
with a 2 GB JVM heap (half the guest, the OpenSearch rule), the same way the
data guests each run one ledger node. All three are master eligible; the
cluster keeps serving with one guest down.

Transport and REST use TLS from the same private CA the ledger uses, one
certificate per node, and the app authenticates with an internal user whose
password the deploy generates once per host next to the ledger password. The
configs repo provisions the guests, issues the certificates, and renders the
URL, user, password, and CA path into `.env`; the tack repo owns the compose
service and the client. That is the existing seam, unchanged.

Model serving on the data nodes needs two cluster settings:
`plugins.ml_commons.only_run_on_ml_node=false` and
`plugins.ml_commons.native_memory_threshold=99`. The provisioning step
registers `huggingface/sentence-transformers/all-MiniLM-L6-v2` version 1.0.2
(384 dimensions, TorchScript, about 90 MB) and deploys it. OpenSearch fetches
the artifact from its own artifact host, so the search guests need outbound
HTTPS to it the way every tack guest already reaches ghcr.io.

## Index

One index, `nodes`, three primary shards, one replica each. A shard is
therefore on two of three nodes and a node loss leaves every shard served.
Three primaries is enough for years at this data size; the split API raises
it later without a reindex.

Mapping:

| Field | Type | Written from |
| --- | --- | --- |
| `org_id` | keyword | the node |
| `scope_ids` | keyword array | every ancestor id up to the org, walked through `parent_id` at index time |
| `node_type` | keyword | the node |
| `name` | text | the node |
| `text` | text | the values of every property whose definition is text, select, multi-select, or URL, joined with newlines; no property name appears in code |
| `embedding` | knn_vector, 384, lucene hnsw, cosine | the ingest pipeline |

The document id is the node id. An ingest pipeline `nodes-embed` with one
`text_embedding` processor computes `embedding` from `name` and `text`, so
the app never touches a vector and the reindex uses the same pipeline through
the bulk API. `Props` leaves the document: the tool prints nodes from the
store, not from the index, and the facet counts nobody reads go with it.

## Query

One request per search, a `hybrid` query with two sub-queries and one filter:

```json
{
  "from": 25, "size": 25,
  "query": {
    "hybrid": {
      "pagination_depth": 1000,
      "filter": { "bool": { "filter": [
        { "term": { "org_id": "<caller org>" } },
        { "term": { "node_type": "<validated type key>" } }
      ] } },
      "queries": [
        { "multi_match": { "query": "<text>", "fields": ["name^3", "text"] } },
        { "neural": { "embedding": { "query_text": "<text>", "model_id": "<id>", "k": 100 } } }
      ]
    }
  }
}
```

The search pipeline `nodes-hybrid` normalizes both score lists with `min_max`
and combines them with `arithmetic_mean`, weights 0.5 and 0.5 to start; the
weights are one setting, tuned on QA against the proof pairs. The filter is
JSON the client library marshals from Go values, never a string, and it
applies to both sub-queries before scoring. The `node_type` term is added
only when the argument names a type in the org's type index; any other value
is refused before the request is built. A later rule such as "hide project X
from group Y" is one more term on `scope_ids` in the same filter. One test
sends a crafted `node_type` and asserts the request body the client sent.

## Paging

Hybrid results page with `from` and `size`, and `pagination_depth` fixes the
candidate set per shard so page two does not reshuffle page one. Sorting by
a field with `search_after` is possible but discards the relevance score, so
it is not used. A page is 25 rows; the cursor is the next offset, base64 like
the list cursors; the deepest reachable row is 1,000 per shard per
sub-query, which the tool states when a cursor runs out. The engine returns
ids; the page reads its 25 views from FoundationDB in one transaction
through a new `ViewStore.GetMany`, drops any view outside the caller's org
(the index is never trusted for isolation), and renders from the store, so a
renamed node prints its current name.

## Writes and rebuild

The node service indexes on create and update and deletes on delete, as it
does now, through the same `Searcher` interface with an OpenSearch client
behind it (`opensearch-go` v4). `ops batch search-reindex` keeps its shape:
per org, per type, 500 views per page from `ListPage`, one bulk request per
page, first error stops the run. A reindex with the pipeline attached is the
recovery path for an empty or damaged cluster and the migration path from
Meilisearch.

## Cutover

1. Configs: three guests per environment, certificates, `.env` values.
2. Tack: the compose service for the search guests, the adapter, the
   provisioning step that creates the index, both pipelines, and the model.
3. QA deploy, then `search-reindex --execute` on QA, then the proofs below.
4. Production deploy, reindex, proofs.
5. Remove the Meilisearch service, its volume, `MEILI_URL`,
   `MEILI_MASTER_KEY`, the adapter, and the 2 GB it held on the app guest.

## Proof

1. Scale: a project with 150 issues returns 25 rows and a cursor; following
   cursors yields all 150 once; one engine request and one FoundationDB
   transaction per page.
2. Isolation: a crafted `node_type` never widens the filter, and a second
   org's nodes never appear in the first org's results.
3. Meaning: "db" finds "Database failover", "biscuits" finds "Cookie banner",
   and five more fixed pairs, on QA after a reindex, with no synonym list.
4. Operations: stop one search guest and search still answers; empty the
   index and `search-reindex` restores it.

## Sources

[OpenSearch hybrid query](https://docs.opensearch.org/latest/query-dsl/compound/hybrid/),
[hybrid pagination](https://docs.opensearch.org/latest/vector-search/ai-search/hybrid-search/pagination/),
[pretrained models](https://docs.opensearch.org/latest/ml-commons-plugin/pretrained-models/),
[Meilisearch Enterprise license](https://www.meilisearch.com/blog/enterprise-license).
