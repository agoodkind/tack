# Search work contracts

FoundationDB supplies authoritative indexing work. OpenSearch stores derived page documents.

## Durable work

Source mutations atomically record desired work. Records persist these fields:

- Class.
- Generation.
- Revision.
- Projection.
- Phase.
- Owner.
- Lease.
- Cursor.

Registration rejects stale state. Workers checkpoint the contiguous successful prefix, retain failures, and refresh new pages before retiring obsolete IDs. Before a page can register, a claim must still match these values:

- Owner.
- Lease.
- Generation.
- Revision.
- Projection.
- Policy versions.
- Target.

A crash repeats the uncommitted slice.

Workers bound slice admission, individual operations, and bulk requests separately.

Work limits:

| Subject | Limit |
| --- | --- |
| Content slice | At most 32 pages or 5 MiB. |
| New remote-operation admission | Workers do not start another operation after two seconds. |
| Admitted operation | At most ten seconds. |
| Cleanup and access slices | At most 100 IDs. |
| Bulk request | Below 500 documents and 5 MiB. |

One operation of at least 5 MiB forms its own bulk request.

Workers schedule these classes by weighted worker time:

- Live.
- Access.
- Cleanup.
- Rescan.
- Rollout.
- Rebuild.
- Copy.

Backpressure leaves work pending. Each node retains its desired generation and current issued IDs. Convergence removes these obsolete records:

- Claims.
- Cursors.
- Errors.
- Issued-ID state.

Retired engine records remain until physical-index deletion.

## Index replacement and retention

Only one replacement runs while its old index awaits retirement. Full replacement creates an empty index and scans current FoundationDB nodes. A native split retains the model, mapping, projection identity, and document schema. Native split permits a larger multiplicative primary count within reserved routing shards and preserves embeddings. Restore requires full replacement and a new restore epoch that invalidates restored cursors.

Replacement uses these durable states:

- Creating.
- Copying.
- Verifying.
- Switching.
- Retiring.
- Failed.

The coordinator persists these values:

- Mode.
- Source and target.
- Shard and replica counts.
- Scan and verification cursors.
- Generation.
- Pause or failure state.

Search writes also update the target during copying, verification, and switching. The coordinator performs these operations in order:

1. It waits for copies.
2. It pauses claims.
3. It checks copies again.
4. It refreshes and verifies a green target.
5. It atomically changes the alias.

After an uncertain response, it reads the alias. The old target allows a retry, the new target allows completion, and any third target signals an error.

The coordinator performs a native split in this order:

1. It pauses new claims.
2. It waits for earlier leases to expire.
3. It blocks source writes.
4. It splits the readable source.
5. It clears the target's inherited write block.
6. It waits for green.
7. It restores source writes.
8. It resumes claims.
9. It verifies the target before switching the alias.

If a split fails, the coordinator performs these recovery operations:

1. It restores source writes.
2. It preserves the source alias.
3. It deletes failed targets.
4. It resumes claims.

Repeated steps inspect engine and FoundationDB state. A pause limit prevents unavailable targets from pausing claims indefinitely.

New sessions use the new serving index. Existing sessions retain the old physical index until expiry. The current worker waits for session cleanup, blocks writes, and deletes the old index. It does not revoke write permissions or disable automatic recreation. Both safeguards remain prerequisites for [accepted retirement](2026-09-19-search-acceptance.md#index-replacement-lifecycle). The environment permits at most one serving, one replacement, and one retiring index.

These changes require full replacement:

- Model.
- Tokenizer.
- Embedding format.
- Mapping.
- Page identity.
- Restore.
- Cleanup.
- Unsupported shard changes.

Text projections use affected-content refresh. Permission versions use access-only transitions. These thresholds can start a full replacement:

- Retired-page count.
- Retirement age.
- Primary bytes.

Engine capacity failures leave work pending.
