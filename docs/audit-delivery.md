# Audit delivery

Tack publishes audit events from database outboxes to Apache Kafka.
An outbox stores events that await publication. The `audit-consumer` service
runs the relay and consumer, writes the permanent ledger in YugabyteDB, and
updates its hash chains.

## Outbox delivery

After Kafka acknowledges publication, the relay removes accepted entries from
the FoundationDB and YugabyteDB outboxes. Publication failures leave entries
available for retry. Kafka acknowledgment confirms publication. Consumers
commit the permanent ledger record afterward.

Tack configures its Kafka producer to wait for all in-sync replicas to
acknowledge each write.
Retries preserve event identifiers. Each ledger transaction claims an event
identifier before appending its record. Redelivery does not append a duplicate.

## Chain integrity

Each hash chain belongs to one organization and shard. Kafka uses that pair as
the partition key. Consumers in the same group divide partitions among
themselves. Before appending a record, each consumer checks the previous chain
head and retries its batch if another writer has advanced that head.

Ledger transactions commit ordinary event records and their database delivery
offset together. Consumers store rejected ordinary records and their rejection
reasons in the dead-letter table. A connection failure stops the batch for retry.

## Large events

Large events use transport parts and a manifest that identifies the complete
event. Consumers save received parts and delivery offsets together, then verify
and reconstruct the complete event before appending it to the ledger. Missing
parts leave the assembly available for completion. Committing the completed
event removes its temporary assembly data.
