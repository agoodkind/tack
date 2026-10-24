# Large node storage

TACK-524 defines the error returned by the current storage format. TACK-525
defines the replacement storage format.

## Problem

Tack serializes each node and node view into one FoundationDB value.
FoundationDB rejects values larger than 100,000 bytes. It also rejects
transactions with more than 10,000,000 bytes of affected data and recommends
redesigning transactions that exceed one megabyte. These limits are documented
in FoundationDB's
[known limitations](https://apple.github.io/foundationdb/known-limitations.html).

The current write path lets FoundationDB discover an oversized value. The MCP
caller then receives an unexpected server error even though reducing the input
would correct it. This must be fixed before the storage format changes.

A larger application limit cannot make one FoundationDB value larger. Tack
must divide a large record across keys while preserving the atomic node, view,
secondary-index, relationship, reference, idempotency, and audit contract.

## Decision

TACK-524 will add a typed `ValueTooLargeError`. The error will contain the
record kind, actual byte count, and allowed byte count. Node create and update
will serialize the node and node view before opening the write transaction and
return this error before writing when either value exceeds 100,000 bytes. The
FoundationDB adapter will also translate error 2103 into the same type. The MCP
boundary will return a recoverable invalid-argument response.

TACK-525 will replace that temporary node limit with a chunked record format.
The public limit will be 8 MiB for a serialized node. The service will apply
defaults and validation before measuring the canonical JSON. A node above that
limit will return `ValueTooLargeError` with the 8 MiB limit. The limit bounds
request memory and work per call; it does not depend on FoundationDB's
single-value limit.

TACK-524 remains part of the final system. It will protect manifests, chunks,
relationship values, idempotency records, and legacy write paths from
FoundationDB key and value limit failures.

## Record format

Values up to 64 KiB will retain the current inline JSON format. Readers will
continue to accept these values without a migration.

Larger nodes will use one generation for the primary node and materialized
view. Their existing `node_instance` and `node_view` keys will contain a small
manifest instead of the serialized record:

```json
{
  "storage": "chunked_node_v1",
  "generation": "<UUIDv7>",
  "record_kind": "node",
  "byte_count": 1258291,
  "chunk_count": 20,
  "sha256": "<hex digest>"
}
```

The view manifest will use `record_kind: "view"` and the same generation. Each
manifest describes its own serialized length, chunk count, and checksum.

Chunks will use these keys:

```text
(node_chunk, org_id, node_type, node_id, generation, record_kind, ordinal)
```

Each value will contain at most 64 KiB of the serialized JSON. The ordinal will
start at zero. A generation will be immutable after its first chunk write.

Staging and cleanup will use separate key spaces:

```text
(node_chunk_stage, org_id, node_id, request_hash)
(node_chunk_gc, versionstamp)
```

A staging record will contain the request fingerprint, generation, expected
previous generation, creation time, and publication state. A cleanup record
will identify one generation whose chunk ranges can be cleared.

## Writes

Create and update will use the MCP session and request identifiers with a
fingerprint of the canonical payload to derive a stable request hash. Lower
level callers must provide an equivalent idempotency token. Reusing a token
with a different fingerprint will return a conflict.

The write path will perform these steps:

1. Serialize the node and view, enforce the 8 MiB public limit, and calculate
   both SHA-256 checksums.
2. Create or resume the staging record. A retry with the same request hash and
   fingerprint will reuse its generation.
3. Write immutable 64 KiB chunks in transactions containing no more than
   512 KiB of chunk values.
4. Read the generation in ordinal order. Reject publication unless the chunk
   counts, byte counts, and checksums match both manifests.
5. In one final transaction, confirm the expected previous generation and
   replace both root values. The same transaction will update secondary
   indexes, references, relationships, idempotency state, and the staged audit
   intent. It will mark the staging record as published and enqueue superseded
   generations for cleanup.

The final transaction is the visibility boundary. Readers will return the
complete previous generation until it commits and the complete new generation
after it commits. Chunks written by an interrupted request remain invisible.

An indexed property value that would make its secondary-index key unsafe will
use a tagged SHA-256 digest in that key. Equality reads will hash the requested
value, load each candidate node, and compare the complete value before
returning it. Human references and other values stored directly in keys will
retain explicit length limits because chunking values cannot extend a
FoundationDB key.

## Reads and paging

A reader will distinguish `chunked_node_v1` manifests from legacy inline JSON.
It will read chunk ranges in ordinal order and verify the manifest before JSON
decoding. A missing chunk, unexpected ordinal, wrong byte count, or checksum
mismatch will return a typed corruption error. No caller will receive partial
JSON.

The reader will stream chunk values into a buffer capped at 8 MiB. Page and
batch readers will also enforce a 16 MiB reconstructed-byte budget. They will
stop before the next complete record would exceed that budget and return a
cursor for that record. One valid record can exceed the page budget and still
be returned by itself. This keeps process memory bounded without making a large
node unreadable.

The public get operation will return the complete node. List rendering will
retain its existing 32 KiB response limit and cursor, even when the underlying
page contains a larger reconstructed view.

## Cleanup and deletion

The cleanup worker will clear chunk ranges only when no live node or view
manifest names that generation. Cleanup will be idempotent.

Published updates will enqueue the previous generation in their final
transaction. Deletes will clear the node and view manifests with the existing
node records and enqueue every published generation for that node. A sweeper
will enqueue unpublished staging records older than 24 hours. A retry can
finish a matching staging record before that deadline.

The cleanup queue is durable. A process crash can delay reclamation but cannot
make an abandoned or superseded generation visible.

## Backup, repair, and search

FoundationDB backup and restore will preserve manifests, chunks, staging
records, and cleanup records because they share the database key space. The
restore drill will reconstruct and verify a large fixture before declaring the
FoundationDB restore usable.

Inspection and repair commands will report each manifest, its chunk count,
total bytes, checksum result, staging state, and cleanup state. Repair will not
publish a generation with missing or invalid chunks.

Search indexing and full reindex will consume the verified reconstructed view.
The search index will store one document per embedding passage and group
results by node ID. No FoundationDB chunk boundary will become a search-text
boundary. Text found only in the final stored chunk must remain searchable.

## Rollout

1. Deploy TACK-524 while all nodes still use inline values.
2. Add dual-format readers and the chunk, staging, and cleanup key spaces.
3. Enable chunked writes for records above 64 KiB. Existing inline records will
   remain readable and will change format only after an update makes them
   large enough.
4. Enable large-record inspection, cleanup, backup validation, and search
   reindexing before raising the public limit to 8 MiB.
5. Run the large-node acceptance criteria in QA before production changes.

The rollout does not require an eager rewrite of existing nodes.
