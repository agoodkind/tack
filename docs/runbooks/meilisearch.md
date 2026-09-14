# Meilisearch recovery

Meilisearch holds the full-text search index for Tack nodes. It stores no
authoritative data: every searchable field is a projection of a node in
FoundationDB, which is the canonical store. Meilisearch is therefore not backed
up. On loss of the Meilisearch volume, the index is rebuilt from FoundationDB.

## How the index stays current

The service layer indexes a node into Meilisearch on every create and update
(`internal/service/node_create_effects.go` calls the searcher). A node written
to FoundationDB is indexed in the same operation, so in steady state the index
matches the canonical data. On startup the app recreates the index and its
filterable attributes through `EnsureIndex`.

## Recovery

Meilisearch recovery is a rebuild, not a restore:

1. Bring up a fresh Meilisearch on an empty volume. The app recreates the index
   and filterable attributes on its next start.
2. Repopulate every node from FoundationDB by running the indexing path over all
   existing nodes.

The recovery point objective is zero, because no unique data lives in
Meilisearch. The recovery time objective is bounded by how long repopulation
takes.

## Current gap

There is no single bulk "reindex into Meilisearch" command. The `reindex` op
(`internal/ops/reindex.go`) rebuilds FoundationDB secondary property indexes,
not the Meilisearch index. Until a bulk Meilisearch reindex op exists,
repopulation depends on re-running writes. The remaining work for a one-command
rebuild is an op that iterates `NodeListView` per org and calls the searcher for
each node.
