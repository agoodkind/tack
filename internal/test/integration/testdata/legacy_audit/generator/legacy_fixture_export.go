package audit

import "context"

// LegacyFixturePartitionKey returns the Kafka key that KafkaRecorder.Record
// produces for ev.
//
// The helper uses the revision's kafkaPartitionKey implementation to capture
// legacy fixtures.
//
//   - Use the returned bytes as the Kafka record key.
//   - Encode the key as hexadecimal for the fixture's PartitionBytes field.
func LegacyFixturePartitionKey(ev Event) []byte { return kafkaPartitionKey(ev) }

// LegacyFixtureOutboxJSON returns the event bytes that PoolOutbox.WriteOutbox
// inserts into public.ops_outbox for ev.
//
// The helper uses the revision's marshalOutboxEvent implementation with a
// background context.
//
//   - Store the returned JSON bytes in the fixture's Value field.
//   - Return the error if event encoding fails.
func LegacyFixtureOutboxJSON(ev Event) ([]byte, error) {
	return marshalOutboxEvent(context.Background(), ev)
}
