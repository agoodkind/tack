# How audit delivery uses Kafka

A product mutation can commit its data and an audit outbox entry together. An admitted read saves its final evidence before releasing the result. The application recorder can publish other events directly to Kafka.

The [complete audit recording contract](../../reference/audit/2026-10-21-audit-completeness-design.md) specifies when each operation must save local evidence.

## Durable delivery

FoundationDB stores product data and its transactional audit outbox. YugabyteDB stores authentication records, the operator outbox, and the compliance ledger. The relays publish queued outbox entries and remove them only after Kafka acknowledges publication. Pending read admissions remain in their lifecycle store until finalization or recovery.

`KafkaRecorder` preserves the event ID and original occurrence time. It serializes the event and calls `ProduceSync` with `AllISRAcks`. It returns producer failures to the caller.

`AllISRAcks` requires acknowledgment from all replicas that currently belong to the in-sync replica set. It does not establish a replica count, cross-host redundancy, consumer completion, or a permanent ledger row. Broker replication and minimum in-sync replica settings determine the available redundancy. A single-replica topic still acknowledges only that replica.

The Kafka client connects lazily. Before serving traffic, the application pings the broker and checks that the configured topic exists and that every partition has a leader and enough in-sync replicas. It retries failed checks within `AUDIT_BROKER_READY_TIMEOUT`, limiting each attempt by `AUDIT_KAFKA_PRODUCE_TIMEOUT`. If the startup budget expires, it closes the producer and returns an error.

## Application recorder

With Kafka configured and the FoundationDB outbox available, the application supplies MCP and authentication handlers with a recorder that preserves the event ID and original time. When a single event enters `SpillRecorder.Record`, Kafka is the first destination. Kafka success completes the call without writing a FoundationDB copy. If Kafka refuses the event, the recorder tries to append it to the FoundationDB outbox for the relay. A successful append returns success. Failure to serialize or append returns the primary and fallback errors. The append has a ten-second deadline independent of request cancellation.

The Kafka producer and spill recorder log different conditions during an outage.

Recorder logging:

| Recorder | Condition | Log level and content |
| --- | --- | --- |
| Kafka producer | First broker refusal | Warning |
| Kafka producer | Later broker refusals | Debug |
| Kafka producer | Successful single publication or published batch without broker refusals | Info; recovery |
| Spill recorder | First successful outbox append in an outage | Error |
| Spill recorder | Later successful appends in that outage | Debug |
| Spill recorder | Each failed append | Error |
| Spill recorder | Next successful single-event Kafka publication during an open spill outage | Info; ends the outage and reports the total spilled |

A buffer wraps this recorder. For `auth.token_used`, it bypasses the memory queue and tries to append the event to the configured durable outbox. Without an outbox, it calls the inner recorder synchronously. It returns any recording error to the caller. For other read-class verbs, it can return after admitting an event to memory, before its batch publishes to Kafka. When the buffer is full, it tries the configured FoundationDB outbox directly. The flusher retains events the backend returns for retry. Admission to this buffer cannot satisfy the durable finalization requirement for a user-data read.

For a batch, the Kafka producer skips each event it cannot serialize and counts it as dropped. It publishes only the events it encoded. If Kafka refuses any encoded event, the spill recorder tries to append each refused event to the outbox and returns the events it could not store. A refusal keeps an existing spill outage open, even if Kafka accepted other events. Only a successful outbox append starts a new spill outage. A batch in which every append fails does not start one.

When the refusal list is empty, the spill recorder clears an open spill outage and logs the count. The Kafka producer can return that list without publishing when every event fails to serialize. The batch recovery log alone cannot establish a successful publication or a permanent ledger row.

Without configured Kafka brokers, the runtime can select a synchronous YugabyteDB recorder. It does not install `SpillRecorder` on that path. If neither backend is configured, the runtime refuses startup unless `AUDIT_ALLOW_UNRECORDED` selects a recorder that discards events. Direct application ledger writes and unrecorded operation violate the production recording contract.

If the authentication middleware cannot record an accepted token-use event, it returns HTTP 503 before calling the next handler. An MCP state-changing tool outside a read collector refuses execution if its request lacks a recorder.

## Chain assignment and retries

The producer computes a shard from the actor ID and event ID. Its Kafka key encodes the organization ID and shard. Events for one chain use the same key. A Kafka consumer group assigns each partition to one consumer at a time.

Shard identity and Kafka partition identity differ. The producer client hashes the key to select a partition. Shard `s` does not necessarily use partition `s`. Several chains may share a partition. Consumer count and partition assignment determine parallel delivery rather than tenant isolation.

The Kafka deployment requires one `audit.events` table and continues each `(org, shard)` chain through `audit.chain_heads`. It does not rename or archive that table and does not create `events_v2`. In that deployment the audit consumer must remain the only permanent ledger writer, including when several consumer instances share the group.

A two-replica QA check must exercise MCP reads and writes and verify the expected permanent events. Check that each affected chain advances by the number of appended events, its previous-hash links remain intact, and the consumer does not create dead-letter rows for successful deliveries.

Each append claims the event ID in `audit.projected_events`. The append transaction computes the row hash, inserts the ledger row, and advances the chain head with a comparison against the previous sequence. A conflicting head update requires a transaction retry. Permanent identity claims prevent a replay from creating another event even if a producer changes its timestamp.

## Consumer transactions

The consumer commits ledger work and the corresponding SQL consumer offset in one YugabyteDB transaction. It commits the Kafka group offset after that transaction succeeds. A failed Kafka offset commit can cause redelivery; the permanent identity claim prevents duplicate ledger rows.

Each record uses a savepoint in the partition's batch transaction. If the consumer rejects the payload as malformed or the ledger refuses the record's data or constraints, the consumer rolls back to that savepoint. It saves a dead-letter row containing the payload and failure reason for later inspection or replay. It increments the dead-letter counter and can commit the SQL offset with the batch.

A chain conflict or an infrastructure failure, such as a missing privilege, table, or connection, fails the batch. The consumer retries eligible chain conflicts. If a batch still fails, it resets the partition's fetch position to the first record in that batch. Consecutive failures double the delay between polls up to 30 seconds. A successful poll with records clears the failure count.

Dead-letter rows do not expire automatically. `ops audit dlq inspect` groups rows by failure and logs an error when it finds any rows. Replay selects the fewest previous attempts first and the oldest rows within each attempt count. It republishes the original payload bytes with a header identifying the original row.

Replay derives the Kafka key by decoding the payload as a plain audit event. For a plain event, it uses the organization from the event context and derives the shard from the actor and event IDs. A decoding error leaves the replay without a key. A multipart part or manifest stores its organization and shard in top-level fields. The initial relay uses those fields directly. The replay decoder can accept the multipart JSON while ignoring both fields. For a nonzero organization, the resulting key differs from the original key required for every message of that event.

The consumer checks the stored payload before it counts an attempt or removes the original row. For a plain event, a matching replay removes the row after successful projection or recognition of the existing event. For a transport message, the consumer can remove the matching dead-letter row after it stores the message, before the complete event exists. Another failure updates the row's attempt count and failure message. Repeated original delivery does not count as an operator replay. The writer can update only the attempt count, last attempt time, and error in a dead-letter row. If a successful replay cannot remove a matching row, the consumer logs a warning.

Replay handling depends on the received record and the state of the original dead-letter row.

Replay handling:

| Received record | Consumer behavior |
| --- | --- |
| Malformed replay header | The consumer logs a warning and processes the record as a fresh delivery. |
| Syntactically valid header for a missing row or different payload | On a failed delivery, the consumer logs a warning, leaves the named row alone, and records the failure under the received record's key. |
| Null Kafka value | The consumer stores a zero-length payload in the dead-letter table. |
| Verified original row disappears before the failed replay updates it | The consumer logs a warning and inserts the failure under the received record's own topic, partition, and offset. |

## Multipart events

Large events use bounded transport parts and a manifest. The consumer commits received parts and delivery offsets together, even when an assembly remains incomplete. A consumed Kafka offset therefore does not prove that the canonical event exists.

## Operation without the broker

Transactional outboxes retain evidence when Kafka cannot accept it. A broker outage must not permit a product change without its required local audit evidence or a read without finalization. Unavailable durable recording requires refusal. Stopping the audit profile and accepting lost audit events is not a valid recovery method.

Permanent ledger completion requires inspection of:

- The canonical event.
- Complete manifests.
- Chain continuity.
- The outcome.

Each of these observations proves a narrower fact than permanent ledger completion:

- Broker acknowledgment.
- A drained group offset.
- A healthy container.
- A merged pull request.

## Deployment boundaries

Configs owns infrastructure, rendered environment, credentials, and the reviewed OpenTofu release path. Tack owns application code and its Compose definitions. An image-only release does not provision a guest, prepare credentials, or apply pending migrations. Run migrations before starting images that require them, including migration 011 for dead-letter replay.

## Consumer startup

The consumer retries its YugabyteDB connection while initial migrations and audit-role preparation make the ledger available. After connection succeeds, it ensures the configured Kafka topic. Producer and consumer configuration must select the same topic. For a fresh topic the consumer requests the 256 partitions specified by its implementation with broker-default replication and the configured retention. It preserves the partition count of an existing topic.

The consumer requests `AUDIT_CONSUMER_TOPIC_RETENTION` (one year by default) for an existing topic. If the broker rejects the retention update, the consumer logs an error and continues with the broker's effective setting. Topic creation and a healthy consumer do not prove the requested retention or replication.

Kafka requires a stable configured cluster identity for its persistent metadata. A container recreation must retain that identity. Separate environments require separate cluster identities rather than reuse of another environment's metadata identity.
