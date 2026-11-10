//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// readReceiptFamilies lists the FoundationDB key families of terminal read
// receipts and their cleanup index. internal/adapters/foundationdb/keys.go
// defines them.
var readReceiptFamilies = []string{"audit_read_receipt", "audit_read_cleanup"}

// redundancyReplicas maps each FoundationDB redundancy mode to its number of
// storage replicas.
var redundancyReplicas = map[string]int64{"single": 1, "double": 2, "triple": 3, "three_data_hall": 3}

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

// rangeBytes returns the summed key and value lengths and the key count of
// every FoundationDB entry that starts with begin.
func rangeBytes(t *testing.T, database fdb.Database, begin []byte) (int64, int64) {
	t.Helper()
	keyRange := fdb.KeyRange{Begin: fdb.Key(begin), End: fdb.Key(append(bytes.Clone(begin), 0xFF))}
	items, err := database.ReadTransact(func(transaction fdb.ReadTransaction) (any, error) {
		return transaction.GetRange(keyRange, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		t.Fatalf("Reading the FoundationDB key range failed; the byte measurement requires every key and value: %v", err)
	}
	var total int64
	entries := items.([]fdb.KeyValue)
	for _, item := range entries {
		total += int64(len(item.Key) + len(item.Value))
	}
	return total, int64(len(entries))
}

// replicationFactor reads the FoundationDB status document and returns the
// number of storage replicas of the cluster configuration.
func replicationFactor(t *testing.T, database fdb.Database) int64 {
	t.Helper()
	document, err := database.ReadTransact(func(transaction fdb.ReadTransaction) (any, error) {
		return transaction.Get(fdb.Key("\xff\xff/status/json")).Get()
	})
	if err != nil {
		t.Fatalf("Reading the FoundationDB status document failed; the replica byte measurement requires it: %v", err)
	}
	var status struct {
		Cluster struct {
			Configuration struct {
				RedundancyMode  string `json:"redundancy_mode"`
				StorageReplicas int64  `json:"storage_replicas"`
			} `json:"configuration"`
		} `json:"cluster"`
	}
	if err := json.Unmarshal(document.([]byte), &status); err != nil {
		t.Fatalf("Decoding the FoundationDB status document failed: %v", err)
	}
	configuration := status.Cluster.Configuration
	if configuration.StorageReplicas > 0 {
		return configuration.StorageReplicas
	}
	replicas, known := redundancyReplicas[configuration.RedundancyMode]
	if !known {
		t.Fatalf("The FoundationDB status document reports redundancy mode %q, which has no known storage replica count", configuration.RedundancyMode)
	}
	return replicas
}

// measureFoundationDB records the key and value bytes under the run's prefix,
// the replication factor, the replica bytes, and the replica bytes per
// terminal read receipt, including the receipt cleanup index.
func (run *scalingRun) measureFoundationDB(t *testing.T) {
	t.Helper()
	database, err := fdbadapter.Open(run.server.clusterFile, 30*time.Second)
	if err != nil {
		t.Fatalf("Opening FoundationDB failed; the byte measurement requires access to the run's key prefix: %v", err)
	}
	run.result.fdbBytes, _ = rangeBytes(t, database, run.prefix)
	run.result.replication = replicationFactor(t, database)
	run.result.replicaBytes = run.result.fdbBytes * run.result.replication
	var receiptBytes, receipts int64
	for _, family := range readReceiptFamilies {
		familyPrefix := append(bytes.Clone(run.prefix), tuple.Tuple{family}.Pack()...)
		familyBytes, familyKeys := rangeBytes(t, database, familyPrefix)
		receiptBytes += familyBytes
		if family == readReceiptFamilies[0] {
			receipts = familyKeys
		}
	}
	run.result.receipts = receipts
	run.result.receiptBytes = perUnit(receiptBytes*run.result.replication, receipts)
}
