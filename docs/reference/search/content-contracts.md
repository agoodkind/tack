# Search content contracts

FoundationDB supplies authoritative nodes and metadata. OpenSearch stores derived page documents.

## Searchable content

Stored metadata determines behavior. Search does not use a product type allowlist or property-name extraction switch. Seeds and QA fixtures provide declarations, but runtime interpretation reads stored metadata.

An explicit `SearchProjection` takes precedence over defaults, including exclusions. Defaults determine the representation of each property type.

Default representations:

| Property type | Default representation |
| --- | --- |
| Text, URL, number, date, and timestamp | Scalars. |
| Select | Option labels. |
| Multi-select | Item labels. |
| Node references, checkbox values, and UUID values | Exclusion. |

An unknown property type requires a declaration. An option without a nonblank label uses its key. The FoundationDB `Indexed` flag controls secondary lookups. It does not select searchable content.

The interpreter renders text in this order:

1. It emits the node name.
2. It orders included properties by projection order and definition UUID.
3. It appends a newline after each leaf.

Rules support these forms:

- Scalars.
- List items.
- Declared object fields.
- Label maps.

Equivalent metadata and values produce deterministic text. This contract excludes these operations:

- Structured property filtering.
- Property sorting.
- Category totals.

Declaration validation checks these constraints:

- Supported modes.
- Item rules.
- Unique object fields.
- Label maps.
- Allowed recursion depth.

Projected pages collectively contain the complete decoded name and included values. One value can span successive reader pages.

The renderer omits invalid, undeclared, and inapplicable values without changing source values. Each omission records these fields:

- Node.
- Property.
- Revision.
- Projection version.
- Reason.

Explicit exclusions do not produce warnings. These conditions fail rendering:

- Malformed rules.
- Incomplete or duplicate definitions.
- Missing node types.
- Unknown types without declarations.

Source-read and engine failures retain their operational errors.

`ops search verify` separates property warnings, authorization exclusions, and stuck work. A verified repair schedules content refresh and clears warnings after convergence. Deletion clears the node's warnings. A missing authoritative parent can exclude a node while replacement finishes. A valid parent schedules indexing again. Engine failures never create authorization exclusions.

Metadata writes update epochs and affected projection state transactionally and schedule bounded rescans against the serving index. Renderer-version changes invalidate old cursors and schedule existing nodes even when declarations remain unchanged. Workers reject inactive renderer or projection formats and reschedule content. A failed metadata read returns an error and prevents publication of an empty tool schema. An organization epoch identifies the metadata used to register its tools. A dispatch after an epoch change requires a complete metadata read. Text projection identity includes these values:

- Pagination and renderer versions.
- Projection format.
- Metadata digest.
- Activation.

An access-policy change does not change this identity.

## Reader pages

Each content response includes these values:

- Node identity.
- Committed revision.
- Projection version.
- Ordinal.
- Cursor.
- Explicit completion.
- Opaque access values.
- Diagnostics.

Workers continue until explicit completion. Page size and previous page counts do not imply completion.

Reader pages and continuations obey byte limits.

Page limits:

| Subject | Limit |
| --- | --- |
| Page | At most 4,096 UTF-8 bytes. |
| Reader overlap | At most one quarter of the page byte bound. |

Continuations preserve character boundaries. Every nonfinal cursor advances. A cursor binds these values:

- Node ID.
- Revision.
- Projection.
- Pagination version.
- Byte bound.
- Offset.
- Ordinal.

A changed revision or projection returns `ErrContentChanged`. Deletion returns `ErrNotFound`. Changed generations or access versions return `ErrWorkChanged`.

Indexing begins before the final source page. Workers do not combine pages or count model tokens. The FoundationDB reader loads a complete node record and renders its text before selecting each page. A bound on page bytes therefore does not bound the memory needed to render a node.

## Native semantic fields

The adapter uses the official stable OpenSearch Go client. Typed APIs handle core operations. Concrete ML Commons requests use official execution and error decoding. A narrow response decoder preserves replacement point-in-time IDs and raw sort JSON. The module dependency supplies the exact client version.

Official clients manage TLS, connections, retries, and endpoint observation. The connection requires a validated CA and credentials. The general client uses the configured request timeout and retry count and observes retryable statuses. Tack configures one HTTPS endpoint and disables member discovery. A separate official predict client avoids timeout retries that repeat inference still running at the engine. A separate official snapshot client sends point-in-time creation once because creation lacks an idempotency key.

Provisioning and verification reject mismatches with the pinned model and tokenizer.

Model identities:

| Subject | Value |
| --- | --- |
| Model | `amazon/neural-sparse/opensearch-neural-sparse-encoding-doc-v3-gte`. |
| Model version | `1.0.0`. |
| TorchScript bundle size | 554,924,400 bytes. |
| TorchScript SHA-256 | `08879b93faf4a92506a44e150f47bbc4cadc9a2f083350c4dc79434738303047`. |
| Tokenizer SHA-256 | `ea725c60b9022a7a491ffc348b5622a199853c806d625f673d0e2ebf1c3b5312`. |

OpenSearch owns tokenization and chunking. The native `semantic` field preserves `page_text` and applies fixed semantic settings.

Semantic settings:

| Setting | Value |
| --- | --- |
| Chunk size | 160 characters. |
| Semantic overlap | Zero. |
| Chunk count | Unlimited. |
| Weight pruning | `max_ratio` at 0.1. |

Reader overlap and semantic overlap differ. Tack does not use any of these components:

- A custom plugin.
- A fork.
- External inference.
- An application-defined ingest pipeline.
- A replacement tokenizer.

The strict mapping rejects unknown fields. Each field has a fixed representation.

Field representations:

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

Document IDs combine these values through length-prefixed bytes and URL-safe base64 encoding:

- Organization.
- Node.
- Revision.
- Text projection.
- Ordinal.

A retry addresses the same document. Permission changes preserve IDs. Mapping metadata records model, tokenizer, and mapping identities. Physical generations record these counts:

- Primary shards.
- Routing shards.
- Replicas.

The mapping version must be nonempty, primary and routing counts must be positive, and replicas must be nonnegative.

