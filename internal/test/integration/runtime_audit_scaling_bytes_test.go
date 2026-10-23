//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// topicBytes sums the partition sizes that Kafka DescribeLogDirs reports for
// the supplied topic.
func topicBytes(t *testing.T, topic string) int64 {
	t.Helper()
	client, err := kgo.NewClient(kgo.SeedBrokers(testenv.Kafka(t)))
	if err != nil {
		t.Fatalf("Creating a Kafka client failed; broker byte measurement requires that client: %v", err)
	}
	defer client.Close()
	request := kmsg.NewPtrDescribeLogDirsRequest()
	response, err := request.RequestWith(t.Context(), client)
	if err != nil {
		t.Fatalf("Kafka DescribeLogDirs failed; broker byte measurement requires the topic's partition sizes: %v", err)
	}
	var total int64
	for _, directory := range response.Dirs {
		for _, described := range directory.Topics {
			if described.Topic != topic {
				continue
			}
			for _, partition := range described.Partitions {
				total += partition.Size
			}
		}
	}
	return total
}

// topicRecords counts records in the run's new topic by summing each
// partition's next record offset.
func topicRecords(t *testing.T, topic string) int64 {
	t.Helper()
	marks, err := audit.TopicHighWaterMarks(t.Context(), []string{testenv.Kafka(t)}, topic)
	if err != nil {
		t.Fatalf("Reading Kafka partition high-water marks failed for topic %s: %v", topic, err)
	}
	var total int64
	for _, mark := range marks {
		total += mark
	}
	return total
}

// prefixBytes sums the key and value lengths for all FoundationDB entries under
// the run's prefix.
func (run *scalingRun) prefixBytes(t *testing.T) int64 {
	t.Helper()
	database, err := fdbadapter.Open(run.server.clusterFile, 30*time.Second)
	if err != nil {
		t.Fatalf("Opening FoundationDB failed; replica byte measurement requires access to the run's key prefix: %v", err)
	}
	keyRange := fdb.KeyRange{Begin: fdb.Key(run.prefix), End: fdb.Key(append(bytes.Clone(run.prefix), 0xFF))}
	items, err := database.ReadTransact(func(transaction fdb.ReadTransaction) (any, error) {
		return transaction.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		t.Fatalf("Reading the run's FoundationDB key range failed; replica byte measurement requires every key and value: %v", err)
	}
	var total int64
	for _, item := range items.([]fdb.KeyValue) {
		total += int64(len(item.Key) + len(item.Value))
	}
	return total
}
