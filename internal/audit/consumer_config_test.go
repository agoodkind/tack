package audit

import (
	"testing"
	"time"
)

// TestApplyConsumerDefaultsPartitionPeriod checks the 24h default and
// preservation of an explicit value.
func TestApplyConsumerDefaultsPartitionPeriod(t *testing.T) {
	got := applyConsumerDefaults(ConsumerConfig{}).PartitionPeriod
	if got != 24*time.Hour {
		t.Fatalf("default PartitionPeriod = %s, want 24h", got)
	}

	got = applyConsumerDefaults(ConsumerConfig{PartitionPeriod: 6 * time.Hour}).PartitionPeriod
	if got != 6*time.Hour {
		t.Fatalf("explicit PartitionPeriod = %s, want 6h", got)
	}
}
