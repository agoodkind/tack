# OpenSearch search acceptance criteria

These criteria decide whether the
[OpenSearch search architecture](2026-09-19-search-design.md) is ready for an
environment. QA must pass every criterion before production changes.
Production must pass them again before Meilisearch is removed.

Measurements must use the MCP client, OpenSearch cluster APIs, application
traces, and FoundationDB reads. OpenSearch results alone cannot prove current
data or tenant isolation.

## Paging and reads

- Create 149 ordinary issues and one large issue in one project. Every issue
  contains the unique phrase `paging probe`. The large issue contains the
  phrase in more than 1,000 passages. A project-scoped search returns six pages
  of 25 issues. The pages contain every issue exactly once.
- Application traces show one OpenSearch request and one byte-bounded
  `ViewStore.GetMany` batch for each page. The read path does not issue one
  FoundationDB transaction per result.

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

- Create an 8 MiB node through the TACK-525 storage path. Place `Application
  terminated unexpectedly` only in the final stored chunk. A search for
  `crash` returns that node on the first page.
- Read every indexed passage after the same write. The ordinals are contiguous
  from zero, every passage has exactly one 384-dimensional vector, no passage
  exceeds 192 model tokens, and the final phrase appears in the final passage.
  The passage count exceeds 100.
- Update the large node. Remove the old final phrase and add `Billing
  reconciliation` only at the end. After indexing converges, `crash` does not
  return the node and `invoice` does. No document from the prior content
  generation remains.
- Delete the large node. No passage document with its node ID remains.
- Record primary-store bytes before and after indexing probe documents with
  known passage counts. Capacity planning uses the measured bytes per passage
  and the measured distribution of passage counts.

## Failure tolerance and recovery

- Each search guest is stopped by itself. For every stopped guest, a search
  returns a known node within the request timeout. A create request completes
  within its timeout, and the new node becomes searchable within 10 seconds.
  The OpenSearch cluster assigns every primary shard and reports non-red
  health.
- `ops batch search-reindex --execute` creates a versioned backing index with
  the normal deployment configuration and switches the `node-passages` alias
  only after the new index passes its checks. All seven semantic query pairs
  and the 8 MiB embedding coverage check pass after the rebuild.
