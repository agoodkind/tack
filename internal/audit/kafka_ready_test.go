package audit

import (
	"context"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// TestReadyRefusesWhenAnyPartitionHasNoLeader answers the review finding that
// one healthy partition is not enough. The producer key hashes an (org, shard)
// pair across the whole partition space, so an event that lands on a
// leaderless partition fails while the topic looks healthy from partition 0.
func TestReadyRefusesWhenAnyPartitionHasNoLeader(t *testing.T) {
	t.Parallel()
	cluster := newFakeCluster(t)
	serveMetadata(cluster, metadataPartition(0, 1), metadataPartition(1, -1))
	recorder := newKafkaRecorderForTest(t, cluster)
	ctx, cancel := context.WithTimeout(context.Background(), testFetchDeadline)
	defer cancel()

	err := recorder.Ready(ctx)

	if err == nil {
		t.Fatal("a topic with a leaderless partition must refuse: events keyed onto it cannot be recorded")
	}
	if !strings.Contains(err.Error(), "partition 1") {
		t.Fatalf("err = %v, want it to name the partition that cannot accept a record", err)
	}
}

// TestReadyRefusesAPartitionlessTopic covers the empty list, which reports no
// error code and no leader problem yet accepts nothing.
func TestReadyRefusesAPartitionlessTopic(t *testing.T) {
	t.Parallel()
	cluster := newFakeCluster(t)
	serveMetadata(cluster)
	recorder := newKafkaRecorderForTest(t, cluster)
	ctx, cancel := context.WithTimeout(context.Background(), testFetchDeadline)
	defer cancel()

	err := recorder.Ready(ctx)

	if err == nil {
		t.Fatal("a topic reporting no partitions must refuse")
	}
	if !strings.Contains(err.Error(), "no partitions") {
		t.Fatalf("err = %v, want it to say the topic reports no partitions", err)
	}
}

// TestReadyAcceptsAFullyLedTopic pins the positive case, so the refusals above
// are proven to come from the partition state and not from the fake itself.
func TestReadyAcceptsAFullyLedTopic(t *testing.T) {
	t.Parallel()
	cluster := newFakeCluster(t)
	serveMetadata(cluster, metadataPartition(0, 1), metadataPartition(1, 1))
	recorder := newKafkaRecorderForTest(t, cluster)
	ctx, cancel := context.WithTimeout(context.Background(), testFetchDeadline)
	defer cancel()

	if err := recorder.Ready(ctx); err != nil {
		t.Fatalf("a topic whose partitions all have leaders must be ready: %v", err)
	}
}

// serveMetadata makes the fake cluster answer every metadata request with the
// given partition state for the audit topic. The broker itself cannot be told
// to strand a partition, so the state under test is injected at the wire.
func serveMetadata(cluster *kfake.Cluster, partitions ...kmsg.MetadataResponseTopicPartition) {
	cluster.ControlKey(kmsg.Metadata.Int16(), func(req kmsg.Request) (kmsg.Response, error, bool) {
		cluster.KeepControl()
		metadataReq, ok := req.(*kmsg.MetadataRequest)
		if !ok {
			return nil, nil, false
		}
		resp := metadataReq.ResponseKind().(*kmsg.MetadataResponse)
		topic := kmsg.NewMetadataResponseTopic()
		name := testKafkaTopic
		topic.Topic = &name
		topic.Partitions = partitions
		resp.Topics = append(resp.Topics, topic)
		return resp, nil, true
	})
}

func metadataPartition(id int32, leader int32) kmsg.MetadataResponseTopicPartition {
	partition := kmsg.NewMetadataResponseTopicPartition()
	partition.Partition = id
	partition.Leader = leader
	if leader >= 0 {
		partition.Replicas = []int32{leader}
		partition.ISR = []int32{leader}
	}
	return partition
}
