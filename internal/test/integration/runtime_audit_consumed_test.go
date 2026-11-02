package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/audit"
)

// topicConsumed reports whether the consumer group's committed offsets match the
// topic high-water marks, which mark the end of each partition. The consumer
// writes each committed offset in the transaction that appends its batch, so
// every record before that offset is in the ledger or the dead-letter table.
func topicConsumed(t *testing.T, reader *audit.Reader, brokers, topic, group string) bool {
	t.Helper()
	marks, err := audit.TopicHighWaterMarks(t.Context(), []string{brokers}, topic)
	if err != nil {
		t.Fatalf("read the high-water marks of %s: %v", topic, err)
	}
	offsets, err := reader.ConsumerOffsets(t.Context())
	if err != nil {
		t.Fatalf("read the consumer offsets: %v", err)
	}
	var produced, consumed int64
	for _, mark := range marks {
		produced += mark
	}
	for _, offset := range offsets {
		if offset.Group == group && offset.Topic == topic {
			consumed += offset.Offset
		}
	}
	return produced > 0 && produced == consumed
}
