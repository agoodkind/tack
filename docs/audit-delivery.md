# Audit delivery

Tack saves audit evidence as events in database outboxes, queues of events
awaiting publication. A relay publishes these events to Apache Kafka. A consumer
reads Kafka messages and saves the audit ledger, Tack's permanent audit records
in YugabyteDB. Both the relay and consumer run in the `audit-consumer` service.

```mermaid
flowchart LR
    Outbox["Database outbox<br/>Events awaiting publication"]
    Kafka["Kafka<br/>Published messages"]
    Ledger["YugabyteDB ledger<br/>Permanent audit records"]
    Outbox -->|Relay publishes| Kafka
    Kafka -->|Consumer saves| Ledger
```

## Publication and recording

Publication is the step when Kafka accepts an audit event. Ledger recording is
the step when the consumer saves it permanently in YugabyteDB.

Outboxes in FoundationDB and YugabyteDB retain events until Kafka acknowledges
publication. Tack waits for acknowledgment from every Kafka replica that
currently keeps a synchronized copy. Outboxes retain events for retry when
publication fails.

Retries preserve the event identifier. When saving an event, the consumer stores
its identifier and ledger record in one database transaction. If that transaction
does not commit, the consumer does not save either item. If the consumer has
already recorded an identifier, it does not append another ledger record for
that event, even if Kafka delivers it again.

## Record order and integrity

A hash chain is an ordered sequence of audit records that links each record to
the one before it. Tack divides each organization's audit history into separate
sequences called shards. Every record stores a calculated value, or hash, that
depends on its contents and the preceding record's hash.
Verification checks these links to detect changes to recorded history.

Kafka uses the organization and shard to assign messages to a partition, an
ordered sequence of messages. Consumers in one group divide these partitions
among themselves.

A chain head stores the sequence number and hash of the latest ledger record.
Consumers check that the head has not changed when they append a record and
update the head. If another writer has advanced it, the consumer retries its
batch against the updated head.

For events that fit in one message, a ledger transaction also saves the delivery
offset, the consumer's position in the Kafka partition. A connection failure
stops the batch for retry.

Consumers save rejected messages and their rejection reasons in a dead-letter
table for investigation instead of appending them to the ledger.

## Events split into parts

Large events are audit events that require several Kafka messages. A manifest
identifies the parts needed to reconstruct the event. Consumers save received
parts and delivery offsets together, then verify and reconstruct the event after
every required part arrives. Consumers retain incomplete events while required
parts are missing. Consumers do not append a partial event to the ledger.

Consumers save the permanent record and remove its temporary parts in one
transaction. Consumers retain the saved parts for retry if that transaction
does not commit.
