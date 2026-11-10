# Search contracts

FoundationDB supplies the authoritative records for these subjects:

- Nodes.
- Metadata.
- Relationships.
- Permissions.
- Sessions.
- Indexing work.

OpenSearch stores and ranks derived page documents. Deployment evidence must establish which release an environment runs. Search must also satisfy the [content contracts](content-contracts.md) and [work contracts](work-contracts.md).

## Public search

`tack_search` accepts these inputs:

- Query text.
- A metadata-defined entry point.
- An optional `node_type`.
- An optional `cursor`.

The node type accepts its key or slug. The handler searches the complete entry point and does not accept a separate scope argument or caller-selected organization.

The disabled handler returns exactly `Search is temporarily unavailable.` before resolving arguments, including exact references and malformed inputs. `OPENSEARCH_PUBLIC_ENABLED` defaults to false and selects public dispatch. Configured index workers continue while public search remains disabled. Enabling public search requires a configured endpoint and cursor key.

The service trims query text and rejects empty, invalid UTF-8, or oversized input before inference. Each process limits simultaneous searches and admission wait. A deadline bounds each request. Engine, source, model, mapping, session, and audit-recording failures return explicit errors.

Each response and engine batch obey the configured limits.

Response limits:

| Subject | Limit |
| --- | --- |
| Distinct nodes per response | At most 25. |
| Response byte budget | Configured; 16,384 bytes by default. |
| Engine batches per response | At most four. |
| Page matches per engine batch | At most 100. |

One bounded FoundationDB summary operation loads each batch's candidates in engine order. The MCP renderer reads current node information. It does not return indexed page text as a node body. The summary reader returns an outcome in input order for each requested ID, including deleted and forbidden nodes. An individual request does not load every match or every visited ID.

## Permission projection

One policy boundary compiles resource and caller keys as sorted, unique, bounded opaque values. Search does not decode keys or inspect these subjects inside keys:

- Permission types.
- Roles.
- Groups.
- Organization IDs.
- Scope IDs.

The current organization-and-scope compiler uses authoritative hierarchy and membership. Another registered policy can interpret permission nodes and relationships without changing document fields. The query rejects these access values:

- Empty values.
- Duplicate values.
- Unsorted values.
- Unsupported values.
- Oversized values.

Each key combines an opaque policy version with the URL-safe base64 encoding of a SHA-256 hash over the length-prefixed version and grant values. The permission-extension example uses a permission-definition node and a relationship between a principal-group node and a resource-root node as a read grant. A compiler interprets that graph and emits matching opaque keys. Search compares the outputs. The production compiler implements organization-and-scope access.

The query requires its access version and at least one caller key before scoring. Type and `retired:false` filters use structured JSON. Current FoundationDB summaries check permission again before returning candidates. Corrupt indexed keys and stale cursors cannot replace that check.

Principal membership changes alter caller keys without scheduling document writes. Resource visibility and ancestry changes schedule access work for affected nodes or a bounded scan. Access updates replace only `search_generation` and `access` without submitting text or invoking the document model. The mapping enables `skip_existing_embedding` for access-only partial updates.

One monotonic per-node generation orders content and access changes. Content and retirement writes read stored generation, sequence number, and primary term before an optimistic write. Tack refuses stale content and prevents writes from restoring a retired page. An older retirement preserves a newer page. OpenSearch rejects changed `if_seq_no` or `if_primary_term` preconditions. The writer rereads the page before another attempt. An access script rejects lower generations. Current-generation retries reuse recorded policy versions.

Each authority runs one candidate transition. Unrelated authorities can transition concurrently. The candidate must be distinct, nonempty, and supported by the policy. New writes include active and candidate versions. The rollout generation guards checkpoints independently of each node's search generation. Bounded scans update pages. Exact verification checks current IDs and generations. Missing, retired, or stale pages require current content work and another check. Concurrent permission events require affected-node verification before activation. Activation compares the verified boundary and selects the candidate for new sessions.

Existing sessions retain their access version and point in time. Retirement waits for those authority-specific sessions, then writes only the active version. A bounded scan and exact page verification at the current permission-event boundary precede old-key cleanup completion. Rollback before retirement restores the previous active version without document recovery. After retirement starts, another candidate transition restores an earlier policy. Access transitions do not create indexes, switch aliases, read text, or regenerate embeddings.

## Ranking and durable continuation

Opening a session verifies the alias against FoundationDB, checks model and mapping identity, predicts one bounded nonempty map of finite nonnegative query weights, and opens a point in time. The predict request supplies the query in `text_docs`. The adapter rejects token output above the configured session byte bound and stores compact token JSON without interpreting token keys. Later batches reuse `query_tokens` without inference.

Ranking adds lexical scores from `name` with boost 3 and `page_text` to the greatest nested sparse score. The lexical `and` operator requires one field to contain every query term before adding its score. A page that matches only some terms receives only the sparse score. The request selects only `node_id` from each hit. OpenSearch uses its sparse inverted index without dense scripts, nearest-neighbor `k`, hybrid windows, collapse, or a total-result cap.

Sorting applies these keys in order:

1. Descending score.
2. Ascending node ID.
3. Ascending `_shard_doc`.

The point in time stabilizes the page tie breaker. The first matching page establishes node rank. Durable visited records suppress later matches. The session submits raw sort JSON unchanged as `search_after`.

Only an empty raw batch completes traversal. Short batches, duplicate-only batches, and empty public responses can still require continuation. The byte limit preserves the first unconsumed eligible candidate. A summary too large for an otherwise empty page cannot fit on any page. The service withholds it, marks it visited, and logs the refusal.

A session hashes length-prefixed values with SHA-256 to bind these values:

- Normalized query.
- Type filter.
- Principal.
- Entry point.
- Permission authority.
- Active version and keys.
- Physical index.
- Ranking version.
- Restore epoch.

A scoring-version change invalidates older continuations and replay. HMAC-SHA256 authenticates cursors. FoundationDB stores these session records:

- Bounded headers.
- Chunked tokens.
- Visited keys.
- Replay records.
- Bucketed expiry and presence keys.

Concurrent calls commit one version or replay its committed result.

Page commitment saves consumed sort position, replacement point-in-time ID, at most 400 visited IDs, at most 25 result IDs, and completion in one FoundationDB transaction before responding. Completion closes the point in time. Replay rereads current summaries and permissions without advancing or renewing the session. It omits deleted, forbidden, and now oversized results. Changed bindings, future versions, restored epochs, missing snapshots, and expired deadlines require a new search.

Session deadlines limit inactivity and total lifetime.

Session deadlines:

| Deadline | Default | Renewal |
| --- | --- | --- |
| Inactivity | Two minutes. | Successful pages renew inactivity only. |
| Point-in-time keep-alive | Two minutes. | Every engine batch renews it. |
| Absolute | Two hours. | Replay and activity cannot extend it. |

`OPENSEARCH_SESSION_IDLE_TIMEOUT` sets both inactivity and point-in-time keep-alive. Configs exposes this deployment setting as `tack_search_session_idle_timeout`. Expired continuations close their point in time and start bounded cleanup. Cleanup marks the session as closing and deletes children before the header. New searches also sweep expired sessions. Later sweeps retry failed cleanup.

## Audit evidence

The shared read contract must admit search durably and finalize each returned page before release. Evidence includes these fields and outcomes:

- Verified actor.
- Criteria.
- Returned identities.
- Counts.
- Fingerprints.
- Outcomes.
- Refusals.
- Failures.

Session, worker, repair, and replacement work preserve initiating identities and record external intent before effects. Recording failure must withhold results or prevent required mutations. A fingerprint does not reconstruct returned content.

Permanent ledger inspection through real public-operation tests must establish completeness. Declarations, logs, healthy endpoints, and unrelated passing tests do not prove that contract.

## Alias switch request

The coordinator submits both alias actions in one request. The example uses placeholder names for the old and new indexes.

```json
{"actions":[{"remove":{"index":"old-physical-index","alias":"node-pages"}},{"add":{"index":"new-physical-index","alias":"node-pages","is_write_index":true}}]}
```
