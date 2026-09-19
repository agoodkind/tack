# OpenSearch search acceptance criteria

These criteria decide whether the
[OpenSearch search architecture](2026-09-19-search-design.md) is ready for an
environment. QA must pass every criterion before production changes.
Production must pass them again before Meilisearch is removed.

Measurements must use the MCP client, OpenSearch cluster APIs, application
traces, and FoundationDB reads. OpenSearch results alone cannot prove current
data or tenant isolation.

## Paging and reads

- Create 150 issues whose names contain the unique phrase `paging probe` in
  one project. A project-scoped search for `paging probe` returns six pages of
  25 issues. The pages contain every issue exactly once.
- Application traces show one OpenSearch request and one FoundationDB
  transaction for each page.

## Tenant and type isolation

- Tack rejects a `node_type` that does not belong to the caller's organization
  before it sends an OpenSearch request.
- Every captured OpenSearch request contains the caller's `org_id` filter.
- A project-scoped request contains the validated project ID in its
  `scope_ids` filter.
- A search never returns the other organization's node when two organizations
  index nodes with the same title.
- A project-scoped search rejects a node from another project in the same
  organization even when its indexed `scope_ids` value falsely includes the
  requested project.

## Semantic ranking

- `Database failover` appears on the first page for `db` after reindexing
  without an organization synonym list.
- `Cookie banner` appears on the first page for `biscuits` under the same
  condition.
- `Authentication failure` appears on the first page for `signin`.
- `Slow request processing` appears on the first page for `lag`.
- `Billing reconciliation` appears on the first page for `invoice`.
- `Application terminated unexpectedly` appears on the first page for
  `crash`.
- `Delete account` appears on the first page for `remove user`.

All seven checks must pass without manually maintained synonyms.

## Complete embedding coverage

- Create a node through Tack with 90,000 bytes of searchable text. Place
  `Application terminated unexpectedly` only at the end. A search for `crash`
  returns that node on the first page.
- Read the indexed node after the same write. `passage_chunk` contains the
  beginning and end of the source text, no passage exceeds 384 tokens, and the
  number of nested vectors equals the number of passages.
- Index a probe document through the deployed `nodes-embed` pipeline with 101
  passages and a semantic target only in passage 101. A nested neural query for
  that target returns the probe. Delete the probe after the check.
- Record primary-store bytes before and after indexing probe documents with
  known passage counts. Capacity planning uses the measured bytes per passage
  and the measured distribution of passage counts.

## Failure tolerance and recovery

- Each search guest is stopped by itself. For every stopped guest, a search
  returns a known node within the request timeout. A create request completes
  within its timeout, and the new node becomes searchable within 10 seconds.
  The OpenSearch cluster assigns every primary shard and reports non-red
  health.
- `ops batch search-reindex --execute` recreates a deleted `nodes` index with
  the normal deployment configuration. All seven semantic query pairs and the
  90,000-byte embedding coverage check pass after the rebuild.
