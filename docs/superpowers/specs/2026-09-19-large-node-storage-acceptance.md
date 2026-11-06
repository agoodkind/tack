# Large node storage acceptance criteria

These criteria decide whether TACK-524 and TACK-525 can advance through QA and
production.

## Temporary size error

- Before chunked storage is enabled, create a node whose serialized node or
  view exceeds 100,000 bytes. The MCP tool returns a recoverable
  invalid-argument response with the record kind, actual bytes, and limit.
- Update an existing node past the same limit. The response uses the same error
  and the previous node, view, indexes, references, relationships, audit state,
  and search projection remain unchanged.
- Force FoundationDB error 2103 through a real transaction path that bypasses
  preflight validation. The MCP boundary returns the typed size error.

## Large node operations

- Create, get, list, update, and delete a node containing more than 100,000
  bytes through the public MCP tools. Every returned complete value matches the
  submitted value.
- Create and update nodes at 128 KiB, 1 MiB, 4 MiB, and 8 MiB. Each operation
  succeeds. A serialized node one byte above 8 MiB returns the typed size error
  without changing visible state.
- Query an indexed property large enough to use a hashed secondary-index key.
  The exact node is returned after full-value verification.

## Publication and retries

- Interrupt a create and an update after each chunk-writing transaction. The
  node remains absent after the create failures, and every update failure
  returns the complete previous generation.
- Interrupt each operation after verification but before manifest publication.
  The same visibility guarantees hold.
- Retry every interrupted request with the same request identity. The retry
  publishes one generation and does not duplicate visible data.
- Reuse one request identity with a different payload. Tack returns a conflict
  and preserves the visible generation.
- Run concurrent readers while an update publishes a new generation. Every
  read returns either the complete old value or the complete new value. No read
  returns mixed chunks or partial JSON.

## Corruption handling

- Remove, reorder, and alter one chunk in separate fixtures. Get, list, backup
  validation, repair inspection, and search reindex return the typed corruption
  error and never return partial content.
- Alter a manifest byte count, chunk count, and checksum in separate fixtures.
  Each mismatch is detected before JSON decoding.

## Cleanup and deletion

- A successful update enqueues and removes the superseded generation after no
  live manifest references it.
- An unpublished staging generation remains invisible. The sweeper enqueues it
  after 24 hours, and cleanup removes its chunks and staging record.
- Repeating cleanup after a crash succeeds without affecting the active
  generation.
- Deleting a node removes its visible records and eventually removes every
  published and staged chunk generation for that node.

## Paging and memory

- List nodes whose reconstructed views would exceed the 16 MiB page budget.
  Every node appears exactly once across cursors, and no page contains a
  partial node.
- Run eight concurrent creates, gets, and updates at each tested size. The
  process remains within its configured memory limit, returns no out-of-memory
  error, and releases temporary payload memory after the requests complete.
- Record throughput, p50 latency, p95 latency, heap use, FoundationDB transaction
  bytes, and chunk cleanup delay for every tested size. Production approval
  requires measured capacity for the expected concurrency.

## Backup, repair, and search

- Back up and restore an 8 MiB node. The restored node and view match their
  manifests and checksums before the restore drill succeeds.
- Inspection reports the active generation, staging generations, cleanup
  generations, chunk counts, byte counts, and checksum verdicts.
- Repair refuses to publish an incomplete generation and can enqueue an
  unreferenced valid generation for cleanup.
- Rebuild an empty search index from FoundationDB. A semantic query returns a
  node whose matching text exists only in the final stored chunk and final
  embedding passage.

All tests must use the public MCP, FoundationDB, backup, repair, and OpenSearch
boundaries with real services. Tests must not replace those dependencies with
mocks, stubs, or recorded responses.
