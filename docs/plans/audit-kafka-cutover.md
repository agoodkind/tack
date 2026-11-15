# Audit delivery

Tack delivers queued audit events from database outboxes to Apache Kafka. The
`audit-consumer` service runs the relay and consumer. The consumer writes the
permanent `audit.events` ledger in YugabyteDB and advances the existing hash
chains in `audit.chain_heads`.

## Outbox delivery

An outbox stores events that await publication. The relay reads the FoundationDB
and YugabyteDB outboxes and publishes their events to Kafka. It removes accepted
entries after Kafka acknowledges publication. A publication failure leaves the
entries available for retry. A broker acknowledgment establishes publication;
the consumer commits the ledger record separately.

The Kafka producer waits for all in-sync replicas to acknowledge each write.
The relay preserves event identities during retries. The consumer claims each
event identifier in the transaction that appends its ledger row. Redelivery
does not append a second row for that identifier.

## Chain integrity

Each hash chain belongs to one organization and shard. Kafka records use that
pair as their partition key. Consumers in the same group divide partitions
among themselves. Chain advancement also checks the previous head and retries a
concurrent change.

The consumer commits ordinary ledger records and its database delivery offset
in one transaction. It retains rejected ordinary records in the dead-letter table
with the reason for rejection. A connection failure stops the batch for retry.

## Large events

Large events use transport parts and a manifest that identifies the complete
event. The consumer saves received parts and delivery offsets together. It
verifies and reconstructs a complete assembly before appending the event to the
ledger. Missing parts leave the assembly available for completion. The consumer
removes temporary assembly data when it commits the completed event.
