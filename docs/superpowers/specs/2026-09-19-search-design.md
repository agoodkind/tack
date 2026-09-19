# OpenSearch search architecture

Epic TACK-517. This design resolves TACK-518, TACK-519, and TACK-520.

## Problem

Production search depends on one Meilisearch 1.12 container with a 2 GB memory
limit. Search stops if that container or its guest stops. Community
Meilisearch 1.12 cannot divide one index across several guests or store a
replica on another guest. Meilisearch offers distributed search under its
[Enterprise Edition license](https://www.meilisearch.com/blog/enterprise-license),
which places sharding under the Business Source License.

The search engine is not a tenant boundary today. The client converts filter
values into a formatted string, and every organization uses the same master
key. The MCP tool provides the final isolation check: it reads each result
from FoundationDB and rejects results whose `org_id` differs from the caller's
organization.

Search also matches words rather than meaning. A query such as `db` does not
match a title such as `Database failover`. TACK-510 tried to close that gap
with organization-specific synonym lists, but that approach required people
to predict and maintain each related term.

Production contained 3,842 indexed nodes after the reindex on 2026-09-19.
That count describes the current deployment. The new architecture must add
capacity with more storage or search nodes without assigning shards in the
Tack application.

## Decision

Tack will replace Meilisearch with OpenSearch 3.8. Each environment will use
three OpenSearch nodes, one `nodes` index, and hybrid keyword and semantic
ranking. OpenSearch will divide every persisted node text into passages and
generate an embedding for every passage on the cluster with a local CPU model.

OpenSearch provides shard placement and replication under the Apache 2.0
license. Its ML Commons plugin can serve
[pretrained sentence-embedding models](https://docs.opensearch.org/latest/ml-commons-plugin/pretrained-models/)
inside the cluster. Search will not depend on an external inference service
because the model will process node text on the search guests.

FoundationDB will remain the source of truth. OpenSearch will contain a
rebuildable projection used for ranking and result IDs. Tack will read the
current node views from FoundationDB before returning a page.

## Cluster

Search must continue to accept reads and writes after any one search guest
stops. Each environment will use this topology:

| Setting | QA | Production |
| --- | --- | --- |
| Hypervisor | suburban | vault |
| Guests | `tack-search1`, `tack-search2`, `tack-search3` | `tack-search1`, `tack-search2`, `tack-search3` |
| Resources per guest | 4 GB memory, 2 cores, 40 GB hot-tier root | 4 GB memory, 2 cores, 40 GB hot-tier root |
| Container | `opensearchproject/opensearch:3.8.0` | `opensearchproject/opensearch:3.8.0` |
| JVM heap | 2 GB | 2 GB |

All three nodes will store data and remain eligible to manage the cluster.
The 2 GB heap follows OpenSearch's guidance to start at half of available
memory in its
[installation settings](https://docs.opensearch.org/latest/install-and-configure/install-opensearch/index/#important-settings).

The private certificate authority used by the ledger will issue one
certificate per search node. OpenSearch will require TLS for REST and
transport traffic. The Tack application will authenticate as an internal
OpenSearch user. Deployment will generate that user's password once per host.

The configs repository will provision the guests and certificates. It will
also render the OpenSearch URL, user, password, and certificate authority path
into `.env`. This repository will own the OpenSearch Compose service, index
configuration, and client.

OpenSearch will run
`huggingface/sentence-transformers/all-MiniLM-L6-v2` version 1.0.2 as a
TorchScript model. The model produces 384-dimensional vectors and requires
about 90 MB of storage. The cluster configuration will set
`plugins.ml_commons.only_run_on_ml_node=false` and
`plugins.ml_commons.native_memory_threshold=99` because the three data nodes
will also run inference. The guests need outbound HTTPS while OpenSearch
downloads the model artifact.

## Index

The index must remain available after one node fails because every shard will
have a copy on a different node. The `nodes` index will have three primary
shards and one replica for each primary. OpenSearch must place each primary
and its replica on different nodes.

The document ID will equal the node ID. Each document will contain these
fields:

| Field | OpenSearch type | Source |
| --- | --- | --- |
| `org_id` | `keyword` | The node's organization ID. |
| `scope_ids` | `keyword` array | Every ancestor ID from the node through its organization, calculated from `parent_id` while indexing. |
| `node_type` | `keyword` | The node's type key. |
| `name` | `text` | The node's current name when indexed. |
| `text` | `text` | Values from text, select, multi-select, and URL properties, joined with newlines. |
| `passage_text` | `text` | The `nodes-embed` ingest pipeline joins `name` and `text`. |
| `passage_chunk` | `text` array | The `nodes-embed` ingest pipeline divides `passage_text`. |
| `passage_embedding` | `nested`; each object contains a 384-dimensional `knn_vector` named `knn`, using Lucene HNSW and cosine similarity | The `nodes-embed` ingest pipeline embeds each value in `passage_chunk`. |

The embedding contract covers all searchable text in every node that Tack
successfully persists. FoundationDB limits each stored node and node view to a
[100,000-byte value](https://apple.github.io/foundationdb/known-limitations.html),
including JSON structure and non-searchable properties. OpenSearch must not
add a smaller text limit or silently omit the end of a valid node.

The application must not create or store vectors. The `nodes-embed` ingest
pipeline will join `name` and `text` into `passage_text`, then use
`text_chunking` to write `passage_chunk` with the
`fixed_token_length` algorithm, the `standard` tokenizer, a 384-token limit,
a 0.1 overlap rate, and `max_chunk_limit=-1`. Disabling the chunk limit
prevents OpenSearch from appending excess input to a final oversized passage.
The pipeline will then use `text_embedding` to generate one nested
`passage_embedding.knn` vector for every value in `passage_chunk`. Both
processors will reject the index request on failure. The application will use
this pipeline for individual writes and bulk reindex requests.

The proposed index document omits the existing `Props` object and facet counts.
Search results need ranked node IDs, and the MCP tool currently discards the
facet counts. Property definitions will determine which values contribute to
`text`; application code will not name individual properties.

Vector storage depends on passage count. Each passage adds 1,536 raw vector
bytes before Lucene and HNSW overhead, and one replica doubles the indexed
storage. QA must measure bytes per passage and the distribution of passage
counts before production capacity decisions. OpenSearch can increase the
primary count with its
[split index API](https://docs.opensearch.org/latest/api-reference/index-apis/split/)
if measured growth exceeds the initial layout.

## Query and isolation

Each search will send one hybrid query. The keyword branch will search `name`
with a boost of 3 and `text` with the default weight. The semantic branch will
use the local embedding model, score every document by its best matching
passage, and request 100 nearest candidates. The `nodes-hybrid` search
pipeline will normalize both score sets with `min_max` and combine them with
`arithmetic_mean`. Both branches will start with a weight of 0.5. QA results
will determine the final weights.

Every request will contain an `org_id` term filter. The application will add
a `node_type` term only after it confirms that the type belongs to the
caller's organization. It will build both terms as structured JSON values.
It will reject an unknown `node_type` before sending the request.

A project, workspace, or other resolved search scope will add that node's ID
as a `scope_ids` term. The existing resolver must confirm that the scope
belongs to the caller's organization before Tack builds the query. Engine-side
scope filtering is required because filtering only 25 returned IDs cannot
produce a complete page from a larger organization. The same filter applies
to both branches of the
[hybrid query](https://docs.opensearch.org/latest/query-dsl/compound/hybrid/).

```json
{
  "from": 25,
  "size": 25,
  "query": {
    "hybrid": {
      "pagination_depth": 1000,
      "filter": {
        "bool": {
          "filter": [
            { "term": { "org_id": "<caller org>" } },
            { "term": { "node_type": "<validated type key>" } },
            { "term": { "scope_ids": "<validated scope id>" } }
          ]
        }
      },
      "queries": [
        { "multi_match": {
          "query": "<text>", "fields": ["name^3", "text"]
        } },
        { "nested": {
          "path": "passage_embedding",
          "score_mode": "max",
          "query": {
            "neural": { "passage_embedding.knn": {
              "query_text": "<text>", "model_id": "<id>", "k": 100
            } }
          }
        } }
      ]
    }
  }
}
```

The request omits the `node_type` or `scope_ids` term when the caller does not
provide that filter. Later visibility rules can add terms to the same Boolean
filter. This design does not define those rules.

## Paging and result reads

Each page must preserve relevance order, render current FoundationDB views,
and exclude nodes outside the caller's organization. Search will return 25
results per page. The cursor will encode the next offset as base64, consistent
with the existing list cursors.

Each page will repeat the same query and search pipeline with a new `from`
value. Hybrid score normalization will remain consistent across the reachable
result set because every page will use `pagination_depth=1000`, as described
in the
[hybrid pagination documentation](https://docs.opensearch.org/latest/vector-search/ai-search/hybrid-search/pagination/).
The tool will report that limit when the next offset would exceed 1,000.

OpenSearch will return ordered node IDs. `ViewStore.GetMany` will read the 25
corresponding views and their ancestor chains in one FoundationDB transaction.
The application will restore the OpenSearch order and render the current
FoundationDB values. It will reject a view whose `org_id` differs from the
caller or whose FoundationDB ancestors do not contain the requested scope.

## Writes and recovery

The node service will update OpenSearch after a node is created or updated and
will delete the document after a node is deleted. The OpenSearch adapter will
implement the existing `Searcher` interface with `opensearch-go` v4.

`ops batch search-reindex` must recreate the index from FoundationDB after a
migration or index loss. It will read 500 views at a time for each organization
and type, then send one bulk request for each page through `nodes-embed`. The
command must stop at the first failed read or bulk request. A failed live index
request will leave the FoundationDB write intact and report the failure. A
later successful reindex will regenerate every passage from that stored view.

The [acceptance criteria](2026-09-19-search-acceptance.md) define the QA and
production gates.
