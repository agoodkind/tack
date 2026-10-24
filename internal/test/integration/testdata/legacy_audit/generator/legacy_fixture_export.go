package audit

import "context"

// LegacyFixturePartitionKey returns the Kafka key that KafkaRecorder.Record
// produces for ev.
func LegacyFixturePartitionKey(ev Event) []byte { return kafkaPartitionKey(ev) }

// LegacyFixtureOutboxJSON returns the public.ops_outbox event bytes that
// PoolOutbox.WriteOutbox inserts for ev.
func LegacyFixtureOutboxJSON(ev Event) ([]byte, error) {
	return marshalOutboxEvent(context.Background(), ev)
}
