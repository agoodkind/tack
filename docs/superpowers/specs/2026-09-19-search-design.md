# Search on OpenSearch

Epic: TACK-517. Defects it resolves: TACK-518 (scale), TACK-519 (isolation),
TACK-520 (semantics).

## Summary

Search moves to an OpenSearch cluster. The cluster has three nodes on three
dedicated guests. It replaces the single Meilisearch container on the app
guest.

The engine shards and replicates the index itself. It ranks by keyword and by
meaning with a model it runs locally. The server adds a visibility filter to
every query from the caller's permissions. Results page through an engine
cursor. Each page is printed from one FoundationDB read. The index is a
rebuildable copy of FoundationDB. It is never a source of truth.

## Why not the current engine

Community Meilisearch cannot shard or replicate. One instance holds one copy of
the data. Sharding exists only in the Enterprise Edition, version 1.37 or
later. Its license allows production use only with a commercial agreement.
Routing orgs across several community instances from the app was rejected. The
app would then own the sharding. OpenSearch shards and replicates natively
under the Apache 2.0 license. It runs embedding models inside the cluster.

Sources: [Meilisearch sharding](https://www.meilisearch.com/docs/learn/multi_search/implement_sharding),
[Meilisearch Enterprise Edition license](https://www.meilisearch.com/blog/enterprise-license).

## Cluster

Three dedicated LXC guests run one OpenSearch node each, on QA and on
production. Their roots sit on the hot-path storage tier because queries are
latency-bound. Three master-eligible nodes give a quorum that survives one lost
guest. Each node has about 4 GB of memory. The production hypervisor had 64 GB
free of 94 GB on September 19, 2026.

The guests, their networking, and their configuration are configs-repo work
under the seam rule. The tack repo owns only the application's use of the
cluster.

One index holds every org's nodes. Each primary shard has one replica. The
primary shard count is set for growth at creation. The index can be split when
a shard outgrows its node.

## Search document

Every node writes one document to the index. The document holds the node id,
the org id, the ids on its scope chain, the type key, and the name. It also
holds every property whose definition has a text, select, multi-select, or URL
type. No property name appears in code. The property definitions decide what
is indexed. The document also holds the vector the local model computes from
the name and the text properties.

## Isolation

The server adds a visibility clause to every query. The clause is structured
query JSON, never a formatted string. Today the clause is one exact match on
the caller's org id. The document keeps its scope-chain ids for a later rule
such as "hide this project from this group". That rule becomes one more clause
on the same query, with no reindex.

The caller supplies free text and an optional type key. The server accepts the
type key only if it names a type of the caller's org. A test sends a crafted
type key and asserts the engine query stayed scoped.

## Meaning

Ranking is hybrid. The engine scores each document by keyword match and by
vector similarity to the query. It merges the two scores. The model is a small
open sentence-embedding model. The cluster's own machine-learning plugin runs
it on the CPU. No node text leaves the LXC. No query costs money. A fixed set
of query and title pairs proves that meaning matches with no hand-written word
pairs. One pair is "db" against "Database failover".

## Paging and printing

A search page is 25 rows with an engine cursor to continue. Following cursors
reaches every match once. The engine returns ids in rank order. The page's
nodes are read from FoundationDB in one transaction and printed from the
store. A renamed node never shows a stale name. No page reads rows one at a
time.

## Indexing and rebuild

The node service writes the search document on every create and update. It
removes the document on delete, as today. The reindex command rebuilds the
whole index from FoundationDB through the paged node reader, per org and per
type. It is the recovery path for an empty or corrupted cluster. The
Meilisearch service, its adapter, and its configuration are removed once the
OpenSearch index is proven on QA.

## Proof

1. Scale: a project with more matches than one page returns 25 rows and a
   cursor. Following cursors returns every match once. A page costs one engine
   call and one FoundationDB transaction.
2. Isolation: a crafted type key cannot widen the visibility clause. Another
   org's nodes never appear.
3. Meaning: the fixed query and title pairs match on QA after a reindex.
4. Operations: search keeps serving after one search guest is lost. The
   reindex command rebuilds the index from FoundationDB on QA.
