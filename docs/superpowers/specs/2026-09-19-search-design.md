# Search on OpenSearch

Epic: TACK-517. Defects it resolves: TACK-518 (scale), TACK-519 (isolation),
TACK-520 (semantics).

## Summary

Search runs on an OpenSearch cluster of three nodes, one per dedicated search
guest, replacing the single Meilisearch container on the app guest. The engine
shards and replicates the index itself, ranks by keyword and by meaning with a
model it runs locally, and receives every query with a visibility filter the
server builds from the caller's permissions. Results page through an engine
cursor, and each page is printed from one batched FoundationDB read of the
nodes it names. The index is a rebuildable projection of FoundationDB, never a
source of truth.

## Why not the current engine

Community Meilisearch has no sharding and no replication; one instance holds
one copy. Sharding exists only in the Enterprise Edition (v1.37 or later) under
a license that permits it in production only with a commercial agreement.
Routing orgs across several community instances from the app was rejected as
sharding the app has to own. OpenSearch shards and replicates natively under
Apache 2.0 and runs embedding models inside the cluster.

## Cluster

Three dedicated LXC guests run one OpenSearch node each, on QA and on
production, with roots on the hot-path storage tier because queries are
latency-bound. Three master-eligible nodes give a quorum that survives one
guest. Each node has about 4 GB of memory. The guests, their networking, and
their configuration are configs-repo work under the seam rule: the tack repo
owns only the application's use of the cluster.

One index holds every org's nodes with one replica per primary shard. The
primary shard count is set for growth at creation and the index can be split
when a shard outgrows its node.

## Search document

Every node writes one document: id, org id, the ids on its scope chain, type
key, name, and every property whose definition has a text, select, multi-select,
or URL type. No property name appears in code; the property definitions decide
what is indexed. The document also holds the vector the local model computes
from the name and text properties.

## Isolation

Every query carries a visibility clause the server builds as structured query
JSON, never as a formatted string. Today the clause is one exact match on the
caller's org id. The document's scope-chain ids exist so that a later rule
such as "hide this project from this group" is one more clause on the same
query, with no reindex. The caller supplies free text and, optionally, a type
key; the type key is accepted only if it names a type of the caller's org. A
test sends a crafted type key and asserts the engine query stayed scoped.

## Meaning

Ranking is hybrid: the engine scores each document by keyword match and by
vector similarity between the query and the document, and merges the two. The
model is a small open sentence-embedding model run by the cluster's own
machine-learning plugin on the CPU, so no node text leaves the LXC and no
query costs money. A fixed set of query and title pairs, such as "db" against
"Database failover", proves meaning matches without any hand-written word
pairs.

## Paging and printing

A search page is 25 rows with an engine cursor to continue; following cursors
reaches every match once. The engine returns the ids in rank order; the page's
nodes are read from FoundationDB in one transaction and printed from the store,
so a renamed node never shows a stale name and no page reads rows one at a
time.

## Indexing and rebuild

The node service writes the search document on every create and update and
removes it on delete, as today. The reindex command rebuilds the whole index
from FoundationDB through the paged node reader, per org and per type, and is
the recovery path for an empty or corrupted cluster. The Meilisearch service,
its adapter, and its configuration are removed once the OpenSearch index is
proven on QA.

## Proof

1. Scale: a project with more matches than one page returns 25 rows and a
   cursor, and following cursors returns every match once; a page costs one
   engine call and one FoundationDB transaction.
2. Isolation: a crafted type key cannot widen the visibility clause; another
   org's nodes never appear.
3. Meaning: the fixed query and title pairs match on QA after a reindex.
4. Operations: losing one search guest leaves search serving; the reindex
   command rebuilds the index from FoundationDB on QA.
