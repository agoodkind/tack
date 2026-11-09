# Search contracts

FoundationDB supplies authoritative nodes, metadata, relationships, permissions, sessions, and indexing work. OpenSearch stores and ranks derived page documents. Deployment evidence must establish which release an environment runs.

## Public search

`tack_search` accepts query text, a metadata-defined entry point, an optional `node_type`, and an optional `cursor`. The node type accepts its key or slug. The handler searches the complete entry point and does not accept a separate scope argument or caller-selected organization.

The disabled handler returns exactly `Search is temporarily unavailable.` before resolving arguments, including exact references and malformed inputs. `OPENSEARCH_PUBLIC_ENABLED` defaults to false and selects public dispatch. Configured index workers continue while public search remains disabled. Enabling public search requires a configured endpoint and cursor key.

The service trims query text and rejects empty, invalid UTF-8, or oversized input before inference. Each process limits simultaneous searches and admission wait. A deadline bounds each request. Engine, source, model, mapping, session, and audit-recording failures return explicit errors.

Each response contains at most 25 distinct nodes within the configured byte budget (16,384 bytes by default). It reads at most four engine batches of at most 100 page matches. One bounded FoundationDB summary operation loads each batch's candidates in engine order. The MCP renderer reads current node information. It does not return indexed page text as a node body. The summary reader returns an outcome in input order for each requested ID, including deleted and forbidden nodes. An individual request does not load every match or every visited ID.

## Searchable content

Stored metadata determines behavior. Search does not use a product type allowlist or property-name extraction switch. Seeds and QA fixtures provide declarations, but runtime interpretation reads stored metadata.

An explicit `SearchProjection` takes precedence over defaults, including exclusions. Defaults render text, URL, number, date, and timestamp values as scalars, select values as option labels, and multi-select values as item labels. Node references, checkbox values, and UUID values default to exclusion. An unknown property type requires a declaration. An option without a nonblank label uses its key. The FoundationDB `Indexed` flag controls secondary lookups. It does not select searchable content.

The interpreter emits the node name first. It orders included properties by projection order and definition UUID, then appends a newline after each leaf. Rules support scalars, list items, declared object fields, and label maps. Equivalent metadata and values produce deterministic text. Structured property filtering, property sorting, and category totals remain outside this contract. Declaration validation checks supported modes, item rules, unique object fields, label maps, and allowed recursion depth.

Projected pages collectively contain the complete decoded name and included values. One value can span successive reader pages.

The renderer omits invalid, undeclared, and inapplicable values without changing source values. Each omission records its node, property, revision, projection version, and reason. Explicit exclusions do not produce warnings. Malformed rules, incomplete or duplicate definitions, missing node types, and unknown types without declarations fail rendering. Source-read and engine failures retain their operational errors.

`ops search verify` separates property warnings, authorization exclusions, and stuck work. A verified repair schedules content refresh and clears warnings after convergence. Deletion clears the node's warnings. A missing authoritative parent can exclude a node while replacement finishes. A valid parent schedules indexing again. Engine failures never create authorization exclusions.

Metadata writes update epochs and affected projection state transactionally and schedule bounded rescans against the serving index. Renderer-version changes invalidate old cursors and schedule existing nodes even when declarations remain unchanged. Workers reject inactive renderer or projection formats and reschedule content. A failed metadata read returns an error and prevents publication of an empty tool schema. An organization epoch identifies the metadata used to register its tools. A dispatch after an epoch change requires a complete metadata read. Text projection identity includes the pagination and renderer versions, projection format, metadata digest, and activation. An access-policy change does not change this identity.

## Reader pages

Each content response includes node identity, committed revision, projection version, ordinal, cursor, explicit completion, opaque access values, and diagnostics. Workers continue until explicit completion. Page size and previous page counts do not imply completion.

Pages contain at most 4,096 UTF-8 bytes. Continuations repeat at most one quarter of that byte bound as reader overlap and preserve character boundaries. Every nonfinal cursor advances. A cursor binds node ID, revision, projection, pagination version, byte bound, offset, and ordinal. A changed revision or projection returns `ErrContentChanged`. Deletion returns `ErrNotFound`. Changed generations or access versions return `ErrWorkChanged`.

Indexing begins before the final source page. Workers do not combine pages or count model tokens. The FoundationDB reader loads a complete node record and renders its text before selecting each page. A bound on page bytes therefore does not bound the memory needed to render a node.

## Native semantic fields

The adapter uses the official stable OpenSearch Go client. Typed APIs handle core operations. Concrete ML Commons requests use official execution and error decoding. A narrow response decoder preserves replacement point-in-time IDs and raw sort JSON. The module dependency supplies the exact client version.

Official clients manage TLS, connections, retries, and endpoint observation. The connection requires a validated CA and credentials. The general client uses the configured request timeout and retry count and observes retryable statuses. Tack configures one HTTPS endpoint and disables member discovery. A separate official predict client avoids timeout retries that repeat inference still running at the engine. A separate official snapshot client sends point-in-time creation once because creation lacks an idempotency key.

The pinned model is `amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte` version `1.0.0`. Its TorchScript bundle contains 554,924,400 bytes with SHA-256 `08879b93faf4a92506a44e150f47bbc4cadc9a2f083350c4dc79434738303047`. The tokenizer SHA-256 is `ea725c60b9022a7a491ffc348b5622a199853c806d625f673d0e2ebf1c3b5312`. Provisioning and verification reject mismatches.

OpenSearch owns tokenization and chunking. The native `semantic` field preserves `page_text`, uses 160-character chunks with zero semantic overlap and unlimited chunk count, and prunes weights with `max_ratio` at 0.1. Reader overlap and semantic overlap differ. Tack does not use a custom plugin, fork, external inference, application-defined ingest pipeline, or replacement tokenizer.

The strict mapping rejects unknown fields. Each field has a fixed representation.

| Field | Representation |
| --- | --- |
| `node_id`, `node_type`, `node_revision`, `projection_version` | Keywords identify the node, type, revision, and text projection. |
| `page_ordinal` | A long integer identifies the page position. |
| `name` | Text provides lexical boosting from a bounded Unicode prefix. |
| `search_generation` | A long integer guards stale writes. |
| `retired` | A Boolean excludes obsolete pages. |
| `access.versions`, `access.keys`, `access.generation` | A strict object stores keyword arrays and a long generation. |
| `page_text` | A native semantic field preserves original text. |
| `page_text_semantic_info.chunks.embedding` | OpenSearch generates nested sparse `rank_features` embeddings. |

Document IDs combine organization, node, revision, text projection, and ordinal through length-prefixed bytes and URL-safe base64 encoding. A retry addresses the same document. Permission changes preserve IDs. Mapping metadata records model, tokenizer, and mapping identities. Physical generations record primary shards, routing shards, and replicas. The mapping version must be nonempty, primary and routing counts must be positive, and replicas must be nonnegative.

## Permission projection

One policy boundary compiles resource and caller keys as sorted, unique, bounded opaque values. Search does not decode keys or inspect permission types, roles, groups, organization IDs, or scope IDs inside these keys. The current organization-and-scope compiler uses authoritative hierarchy and membership. Another registered policy can interpret permission nodes and relationships without changing document fields. The query rejects empty, duplicate, unsorted, unsupported, or oversized access values.

Each key combines an opaque policy version with the URL-safe base64 encoding of a SHA-256 hash over the length-prefixed version and grant values. The permission-extension example uses a permission-definition node and a relationship between a principal-group node and a resource-root node as a read grant. A compiler interprets that graph and emits matching opaque keys. Search compares the outputs. The production compiler implements organization-and-scope access.

The query requires its access version and at least one caller key before scoring. Type and `retired:false` filters use structured JSON. Current FoundationDB summaries check permission again before returning candidates. Corrupt indexed keys and stale cursors cannot replace that check.

Principal membership changes alter caller keys without scheduling document writes. Resource visibility and ancestry changes schedule access work for affected nodes or a bounded scan. Access updates replace only `search_generation` and `access` without submitting text or invoking the document model. The mapping enables `skip_existing_embedding` for access-only partial updates.

One monotonic per-node generation orders content and access changes. Content and retirement writes read stored generation, sequence number, and primary term before an optimistic write. Tack refuses stale content and prevents writes from restoring a retired page. An older retirement preserves a newer page. OpenSearch rejects changed `if_seq_no` or `if_primary_term` preconditions. The writer rereads the page before another attempt. An access script rejects lower generations. Current-generation retries reuse recorded policy versions.

Each authority runs one candidate transition. Unrelated authorities can transition concurrently. The candidate must be distinct, nonempty, and supported by the policy. New writes include active and candidate versions. The rollout generation guards checkpoints independently of each node's search generation. Bounded scans update pages. Exact verification checks current IDs and generations. Missing, retired, or stale pages require current content work and another check. Concurrent permission events require affected-node verification before activation. Activation compares the verified boundary and selects the candidate for new sessions.

Existing sessions retain their access version and point in time. Retirement waits for those authority-specific sessions, then writes only the active version. A bounded scan and exact page verification at the current permission-event boundary precede old-key cleanup completion. Rollback before retirement restores the previous active version without document recovery. After retirement starts, another candidate transition restores an earlier policy. Access transitions do not create indexes, switch aliases, read text, or regenerate embeddings.

## Ranking and durable continuation

Opening a session verifies the alias against FoundationDB, checks model and mapping identity, predicts one bounded nonempty map of finite nonnegative query weights, and opens a point in time. The predict request supplies the query in `text_docs`. The adapter rejects token output above the configured session byte bound and stores compact token JSON without interpreting token keys. Later batches reuse `query_tokens` without inference.

Ranking adds lexical scores from `name` with boost 3 and `page_text` to the greatest nested sparse score. The lexical `and` operator requires one field to contain every query term before adding its score. A page that matches only some terms receives only the sparse score. The request selects only `node_id` from each hit. OpenSearch uses its sparse inverted index without dense scripts, nearest-neighbor `k`, hybrid windows, collapse, or a total-result cap.

Sorting uses descending score, ascending node ID, then ascending `_shard_doc`. The point in time stabilizes the page tie breaker. The first matching page establishes node rank. Durable visited records suppress later matches. The session submits raw sort JSON unchanged as `search_after`.

Only an empty raw batch completes traversal. Short batches, duplicate-only batches, and empty public responses can still require continuation. The byte limit preserves the first unconsumed eligible candidate. A summary too large for an otherwise empty page cannot fit on any page. The service withholds it, marks it visited, and logs the refusal.

A session hashes length-prefixed values with SHA-256 to bind the normalized query, type filter, principal, entry point, permission authority, active version and keys, physical index, ranking version, and restore epoch. A scoring-version change invalidates older continuations and replay. HMAC-SHA256 authenticates cursors. FoundationDB stores bounded headers, chunked tokens, visited keys, replay records, and bucketed expiry and presence keys. Concurrent calls commit one version or replay its committed result.

Page commitment saves consumed sort position, replacement point-in-time ID, at most 400 visited IDs, at most 25 result IDs, and completion in one FoundationDB transaction before responding. Completion closes the point in time. Replay rereads current summaries and permissions without advancing or renewing the session. It omits deleted, forbidden, and now oversized results. Changed bindings, future versions, restored epochs, missing snapshots, and expired deadlines require a new search.

The default inactivity deadline and point-in-time keep-alive are two minutes. `OPENSEARCH_SESSION_IDLE_TIMEOUT` sets both. Configs exposes this deployment setting as `tack_search_session_idle_timeout`. Every engine batch renews the point-in-time keep-alive. The default absolute deadline is two hours. Successful pages renew inactivity only. Replay and activity cannot extend the absolute deadline. Expired continuations close their point in time and start bounded cleanup. Cleanup marks the session as closing and deletes children before the header. New searches also sweep expired sessions. Later sweeps retry failed cleanup.

## Durable work

Source mutations atomically record desired work. Records persist class, generation, revision, projection, phase, owner, lease, and cursor. Registration rejects stale state. Workers checkpoint the contiguous successful prefix, retain failures, and refresh new pages before retiring obsolete IDs. A claim must still match its owner, lease, generation, revision, projection, policy versions, and target before a page can register. A crash repeats the uncommitted slice.

Content slices permit at most 32 pages or 5 MiB. Workers do not start another remote operation after two seconds. An admitted operation can use at most ten seconds. Cleanup and access slices process at most 100 IDs. Bulk requests stay below 500 documents and 5 MiB, except that one operation at least 5 MiB forms its own request.

Workers schedule live, access, cleanup, rescan, rollout, rebuild, and copy classes by weighted worker time. Backpressure leaves work pending. Each node retains its desired generation and current issued IDs. Convergence removes obsolete claims, cursors, errors, and issued-ID state. Retired engine records remain until physical-index deletion.

## Index replacement and retention

Only one replacement runs while its old index awaits retirement. Full replacement creates an empty index and scans current FoundationDB nodes. A native split retains the model, mapping, projection identity, and document schema. Native split permits a larger multiplicative primary count within reserved routing shards and preserves embeddings. Restore requires full replacement and a new restore epoch that invalidates restored cursors.

Durable states are creating, copying, verifying, switching, retiring, and failed. The coordinator persists the mode, source and target, shard and replica counts, scan and verification cursors, generation, and pause or failure state. Search writes also update the target during copying, verification, and switching. The coordinator waits for copies, pauses claims, checks copies again, refreshes and verifies a green target, and atomically changes the alias. After an uncertain response, it reads the alias. The old target allows a retry, the new target allows completion, and any third target signals an error.

Failed splits restore source writes, preserve the source alias, delete failed targets, and resume claims. The coordinator pauses new claims, waits for earlier leases to expire, blocks source writes, splits the readable source, clears the target's inherited write block, and waits for green. It restores source writes, resumes claims, and verifies the target before switching the alias. Repeated steps inspect engine and FoundationDB state. A pause limit prevents unavailable targets from pausing claims indefinitely.

New sessions use the new serving index. Existing sessions retain the old physical index until expiry. The current worker waits for session cleanup, blocks writes, and deletes the old index. It does not revoke write permissions or disable automatic recreation. Both safeguards remain prerequisites for [accepted retirement](2026-09-19-search-acceptance.md#index-replacement-lifecycle). The environment permits at most one serving, one replacement, and one retiring index.

Model, tokenizer, embedding format, mapping, page identity, restore, cleanup, and unsupported shard changes require full replacement. Text projections use affected-content refresh. Permission versions use access-only transitions. Retired-page count, retirement age, and primary-byte thresholds can start a full replacement. Engine capacity failures leave work pending.

## Audit evidence

The shared read contract must admit search durably and finalize each returned page before release. Evidence includes verified actor, criteria, returned identities, counts, fingerprints, outcomes, refusals, and failures. Session, worker, repair, and replacement work preserve initiating identities and record external intent before effects. Recording failure must withhold results or prevent required mutations. A fingerprint does not reconstruct returned content.

Permanent ledger inspection through real public-operation tests must establish completeness. Declarations, logs, healthy endpoints, and unrelated passing tests do not prove that contract.

## Alias switch request

The coordinator submits both alias actions in one request. The example uses placeholder names for the old and new indexes.

```json
{"actions":[{"remove":{"index":"old-physical-index","alias":"node-pages"}},{"add":{"index":"new-physical-index","alias":"node-pages","is_write_index":true}}]}
```
