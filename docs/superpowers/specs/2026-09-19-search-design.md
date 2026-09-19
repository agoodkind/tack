# OpenSearch search architecture

Epic TACK-517. This design resolves TACK-518, TACK-519, and TACK-520. It also
defines how search indexes the large nodes introduced by TACK-525.

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
three OpenSearch nodes, one `node-passages` alias, and hybrid keyword and
semantic ranking. Tack will divide every persisted node text into passages.
OpenSearch will store and embed one document for each passage with a local CPU
model. The number of passages per node will have no separate search limit.

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
TorchScript model. The model produces 384-dimensional vectors, requires about
90 MB of storage, and declares a
[maximum sequence length of 256 tokens](https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/blob/main/sentence_bert_config.json).
The Tack deployment will pin the model's `tokenizer.json` and checksum beside
the model version. The cluster configuration will set
`plugins.ml_commons.only_run_on_ml_node=false` and
`plugins.ml_commons.native_memory_threshold=99` because the three data nodes
will also run inference. The guests need outbound HTTPS while OpenSearch
downloads the model artifact.

## Index

The index must remain available after one node fails because every shard will
have a copy on a different node. Each versioned backing index for the
`node-passages` alias will have three primary shards and one replica for each
primary. OpenSearch must place each primary and its replica on different
nodes.

Each OpenSearch document will represent one passage. Its document ID will
combine the node ID, content generation, and passage ordinal. The content
generation will be the SHA-256 digest of the canonical search projection.
Each document will contain these fields:

| Field | OpenSearch type | Source |
| --- | --- | --- |
| `node_id` | `keyword` | The source node ID. |
| `content_generation` | `keyword` | The SHA-256 digest of the canonical search projection. |
| `passage_ordinal` | `integer` | The zero-based position of this passage in the projection. |
| `org_id` | `keyword` | The node's organization ID. |
| `scope_ids` | `keyword` array | Every ancestor ID from the node through its organization, calculated from `parent_id` while indexing. |
| `node_type` | `keyword` | The node's type key. |
| `name` | `text` | The node's current name when indexed. |
| `passage_text` | `text` | One bounded passage from the canonical search projection. |
| `embedding` | 384-dimensional `knn_vector`, using Lucene HNSW and cosine similarity | The `node-passages-embed` ingest pipeline embeds `passage_text`. |

The embedding contract covers all searchable text in every node that Tack
successfully persists, including an 8 MiB node stored through TACK-525.
OpenSearch must not add a passage-count limit or silently omit the end of a
valid node.

The application must not create or store vectors. It will build the canonical
search projection by joining the node name and searchable property values in
property order. It will use the pinned model tokenizer to divide that
projection into passages of at most 192 model tokens with a 32-token overlap.
This bound leaves room below the model's 256-token maximum for special tokens.
The application will continue until it consumes the complete projection. It
will not apply a maximum passage count.

The `node-passages-embed` ingest pipeline will use `text_embedding` to write
one `embedding` vector from each document's `passage_text`. The processor will
reject the index request on failure. The application will use this pipeline
for individual writes and bulk reindex requests. This design does not use the
`text_chunking` processor or nested vectors because both representations keep
every passage under one OpenSearch source document and impose a per-document
ceiling.

The proposed index document omits the existing `Props` object and facet counts.
Search results need ranked node IDs, and the MCP tool currently discards the
facet counts. Property definitions will determine which values contribute to
the canonical search projection; application code will not name individual
properties.

Vector storage depends on total passage count. Each passage adds 1,536 raw
vector bytes before repeated metadata, Lucene, and HNSW overhead. One replica
doubles the indexed storage. QA must measure bytes per passage and the
distribution of passage counts before production capacity decisions.
OpenSearch can increase the primary count with its
[split index API](https://docs.opensearch.org/latest/api-reference/index-apis/split/)
if measured growth exceeds the initial layout.

## Query and isolation

Each search will send one hybrid query. The keyword branch will search `name`
with a boost of 3 and `passage_text` with the default weight. The semantic
branch will use the local embedding model and request 100 nearest passage
candidates. OpenSearch will collapse both branches by `node_id` and retain the
best-scoring passage for each node. The `node-passages-hybrid` search pipeline
will normalize both score sets with `min_max` and combine them with
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
          "query": "<text>", "fields": ["name^3", "passage_text"]
        } },
        { "neural": { "embedding": {
          "query_text": "<text>", "model_id": "<id>", "k": 100
        } } }
      ]
    }
  },
  "collapse": { "field": "node_id" }
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
The request will also use OpenSearch's
[hybrid result collapsing](https://docs.opensearch.org/latest/vector-search/ai-search/hybrid-search/collapse/)
to return one result per node. The tool will report the 1,000-result limit when
the next offset would exceed it. It will not expose OpenSearch's passage hit
count as a node count.

OpenSearch will return ordered node IDs. `ViewStore.GetMany` will read the 25
corresponding views and their ancestor chains through one byte-bounded batch.
That batch may use several FoundationDB transactions for chunked TACK-525
views. The application will restore the OpenSearch order and render the
current FoundationDB values. It will reject a view whose `org_id` differs from
the caller or whose FoundationDB ancestors do not contain the requested scope.

## Writes and recovery

The node service will update OpenSearch after a node is created or updated.
It will index every passage for the new content generation before it deletes
documents with the same `node_id` and a different `content_generation`. A
partial bulk failure will leave the prior generation searchable and report the
failure. A later update or reindex will replace it. Node deletion will delete
every passage with the node ID. The OpenSearch adapter will implement the
existing `Searcher` interface with `opensearch-go` v4.

`ops batch search-reindex` must create a new versioned index from FoundationDB
after a migration or index loss. It will stream reconstructed views and flush
each bulk request after 500 passage documents or 5 MiB of serialized request
data, whichever comes first. It will use `node-passages-embed` for every bulk.
The command must stop at the first failed read or bulk request. After the
complete rebuild passes its checks, the command will atomically switch the
`node-passages` alias to the new index. A failed live index request will leave
the FoundationDB write intact and report the failure. A later successful
reindex will regenerate every passage from the stored view.

The [acceptance criteria](2026-09-19-search-acceptance.md) define the QA and
production gates.
