# Search on OpenSearch

Epic TACK-517. Resolves TACK-518, TACK-519, TACK-520. Acceptance criteria
are in [the acceptance page](2026-09-19-search-acceptance.md); the cutover
order is in the implementation plan.

## Problem

Search runs in one Meilisearch 1.12 container on the app guest with a 2 GB
memory limit. Three defects follow from that engine.

1. Capacity is one guest. Community Meilisearch has no sharding or
   replication. Sharding exists in the Enterprise Edition from 1.37, and its
   [license](https://www.meilisearch.com/blog/enterprise-license) allows
   production use only under a commercial agreement. Routing orgs across
   several community instances from the app was rejected because the app
   would then own shard placement.
2. The engine does not enforce tenancy. The org filter is a string built with
   `fmt.Sprintf` that includes the caller's `node_type` argument, and one
   master key serves every org. The control that keeps other orgs out is in
   the tool, not the engine: it re-reads every hit from FoundationDB and
   removes results outside the caller's org.
3. Matching is lexical. Lexical matching does not relate `db` to
   `Database failover`. The synonym feature that tried to cover this
   (TACK-510) required hand-written word pairs and was reverted.

Production holds 3,842 indexed nodes after the 2026-09-19 reindex. Tack is a
multi-tenant product, and the requirement is that index capacity grows by
adding guests rather than by enlarging one, so the current count does not
size the design.

## Decision

Replace Meilisearch with an OpenSearch 3.8 cluster of three nodes on three
new guests, one index, and hybrid keyword and vector ranking with an
embedding model the cluster runs on its own CPUs. OpenSearch is Apache 2.0
and shards and replicates without a license. Its ml-commons plugin serves
[pretrained sentence-embedding models](https://docs.opensearch.org/latest/ml-commons-plugin/pretrained-models/)
in process, so no node text leaves the guests and the design incurs no
external inference API fee.

## Cluster

Each environment gets three LXC guests, `tack-search1` to `tack-search3`,
on the suburban hypervisor for QA and vault for production, with 4 GB of
memory, 2 cores, and a 40 GB root on the hot storage tier. Vault had 64 GB
of memory free on 2026-09-19. Each guest runs one
`opensearchproject/opensearch:3.8.0` container from the tack compose file,
the same shape as one ledger node per data guest. The JVM heap is 2 GB,
which follows the
[install guidance](https://docs.opensearch.org/latest/install-and-configure/install-opensearch/index/#important-settings)
of half the system memory. All three nodes are master eligible. The
cluster must continue serving reads and writes after one guest stops.

Transport and REST traffic use TLS from the private certificate authority
the ledger already uses, with one certificate per node. The app
authenticates as an internal user whose password the deploy generates once
per host, next to the ledger password. The configs repo provisions the
guests, issues the certificates, and renders the URL, user, password, and
CA path into `.env`. The tack repo owns the compose service and the client.

Serving a model on the data nodes needs two cluster settings,
`plugins.ml_commons.only_run_on_ml_node=false` and
`plugins.ml_commons.native_memory_threshold=99`. The provisioning step
registers `huggingface/sentence-transformers/all-MiniLM-L6-v2` version
1.0.2 (384 dimensions, TorchScript, about 90 MB) and deploys it. OpenSearch
downloads the artifact from its own artifact host, so the search guests
need outbound HTTPS to it, as every tack guest already has to ghcr.io.

## Index

One index, `nodes`, with three primary shards and one replica per primary.
Every shard is then on two of the three nodes, and one node loss leaves
every shard served.

Sizing: a document is about 4 KB (a 384-float vector of 1.5 KB, its HNSW
graph entry, the name, and the text properties). One million nodes is
about 4 GB of primaries and 8 GB with replicas, or under 3 GB per node,
which fits the 40 GB roots. Beyond that the roots grow or the
[split API](https://docs.opensearch.org/latest/api-reference/index-apis/split/)
raises the primary count by a multiple with a rehashing pass and no
reindex.

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
the app never handles a vector and the reindex attaches the same pipeline
to its bulk requests. The index omits `Props` and the facet counts: the
tool prints nodes from the store, and it discards the facet return value
today (`docs, _, err := searcher.Search(...)` in the search tool).

## Query

Every search must reach the engine with an org filter the engine applies
before scoring, built from Go values and never from a formatted string. One
request per search, a
[hybrid query](https://docs.opensearch.org/latest/query-dsl/compound/hybrid/)
with two sub-queries and one filter:

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

The `filter` parameter applies to both sub-queries. The search pipeline
`nodes-hybrid` normalizes both score lists with `min_max` and combines them
with `arithmetic_mean` at weights 0.5 and 0.5; the weights are one setting,
tuned on QA against the acceptance pairs. The `node_type` term is present
only when the argument names a type in the org's type index; any other
value is refused before the request is built. A later rule such as "hide
project X from group Y" is one more term on `scope_ids` in the same filter.

## Paging

Each page must preserve relevance order, print current FoundationDB views,
and exclude every node outside the caller's org. A page is 25 rows and the
cursor is the next offset, base64 like the list cursors.

Hybrid results page with `from` and `size`, and
[`pagination_depth`](https://docs.opensearch.org/latest/vector-search/ai-search/hybrid-search/pagination/)
fixes the candidate set per shard so page two does not reorder page one.
`search_after` on a field is possible but returns a null score, so it is
not used. The deepest reachable row is 1,000 per shard per sub-query, and
the tool says so when a cursor runs out. The engine returns ids; the page
reads its 25 views from FoundationDB in one transaction through a new
`ViewStore.GetMany`, drops any view outside the caller's org, and renders
from the store, so a renamed node prints its current name.

## Writes and rebuild

The node service must index on create and update and delete on delete, as
it does now, through the same `Searcher` interface with an OpenSearch
client (`opensearch-go` v4) behind it. `ops batch search-reindex` must
restore an empty or damaged cluster and migrate the current Meilisearch
data. It keeps its shape: per org, per type, 500 views per page from
`ListPage`, one bulk request per page with the ingest pipeline attached,
and the first error stops the run.
