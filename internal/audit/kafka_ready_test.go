package audit

import (
	"context"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// TestReadyRefusesWhenAnyPartitionHasNoLeader covers the review finding
// that one healthy partition does not establish readiness. Organization and
// shard keys distribute events across all partitions. A leaderless partition
// fails its assigned events even when partition 0 remains healthy.
func TestReadyRefusesWhenAnyPartitionHasNoLeader(t *testing.T) {
	t.Parallel()
	cluster := newFakeCluster(t)
	serveMetadata(cluster, metadataPartition(0, 1), metadataPartition(1, -1))
	recorder := newKafkaRecorderForTest(t, cluster)
	ctx, cancel := context.WithTimeout(context.Background(), testFetchDeadline)
	defer cancel()

	err := recorder.Ready(ctx)
	if err == nil {
		t.Fatal(
			"a topic with a leaderless partition must refuse: events keyed onto it cannot be recorded",
		)
	}

	if !strings.Contains(err.Error(), "partition 1") {
		t.Fatalf("err = %v, want it to name the partition that cannot accept a record", err)
	}
}

// TestReadyRefusesAPartitionlessTopic checks an empty partition list. That
// list reports neither an error code nor a leader problem, but the topic
// cannot accept events.
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

// TestReadyAcceptsAFullyLedTopic checks the positive case with the same
// fake cluster. Successful readiness establishes that the rejection tests
// fail because of partition state rather than the fixture.
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

// serveMetadata answers every audit-topic metadata request with the supplied
// partition state. The fake broker cannot create a leaderless partition
// directly, and this helper substitutes the protocol response.
func serveMetadata(cluster *kfake.Cluster, partitions ...kmsg.MetadataResponseTopicPartition) {
	cluster.ControlKey(
		kmsg.Metadata.Int16(),
		func(req kmsg.Request) (kmsg.Response, error, bool) {
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
		},
	)
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

// TestReadyRefusesAPartitionWithNoInSyncReplica checks the in-sync replica
// requirement in addition to leader existence. The producer requires
// acknowledgments from all in-sync replicas. An empty in-sync set rejects
// each produce even when metadata reports a leader.
func TestReadyRefusesAPartitionWithNoInSyncReplica(t *testing.T) {
	t.Parallel()
	cluster := newFakeCluster(t)
	led := metadataPartition(0, 1)
	stranded := metadataPartition(1, 1)
	stranded.ISR = nil
	serveMetadata(cluster, led, stranded)
	recorder := newKafkaRecorderForTest(t, cluster)
	ctx, cancel := context.WithTimeout(context.Background(), testFetchDeadline)
	defer cancel()

	err := recorder.Ready(ctx)
	if err == nil {
		t.Fatal(
			"a partition with no in-sync replica must refuse: acks=all rejects every produce to it",
		)
	}

	if !strings.Contains(err.Error(), "0 in-sync replicas") {
		t.Fatalf("err = %v, want it to name the empty in-sync set", err)
	}
}

// TestReadyRefusesAPartitionBelowMinInSyncReplicas checks the configured
// minimum in addition to leader and in-sync replica counts. The producer
// requires acknowledgments from all in-sync replicas. The broker rejects
// each produce with NOT_ENOUGH_REPLICAS below the topic minimum, even when
// metadata otherwise appears healthy.
func TestReadyRefusesAPartitionBelowMinInSyncReplicas(t *testing.T) {
	t.Parallel()
	cluster := newFakeCluster(t)
	serveMinInSyncReplicas(cluster, "2")
	recorder := newKafkaRecorderForTest(t, cluster)
	ctx, cancel := context.WithTimeout(context.Background(), testFetchDeadline)
	defer cancel()

	err := recorder.Ready(ctx)
	if err == nil {
		t.Fatal(
			"a partition below the topic's min.insync.replicas must refuse: acks=all rejects every produce to it",
		)
	}

	if !strings.Contains(err.Error(), "below the topic minimum of 2") {
		t.Fatalf("err = %v, want it to name the minimum the partition falls short of", err)
	}
}

// TestReadyRefusesAnUnreadableMinInSyncReplicas checks that an unreadable
// setting fails readiness. Assuming a default would permit startup without
// verifying the topic's acknowledgment requirement.
func TestReadyRefusesAnUnreadableMinInSyncReplicas(t *testing.T) {
	t.Parallel()
	cluster := newFakeCluster(t)
	serveMinInSyncReplicas(cluster, "not-a-number")
	recorder := newKafkaRecorderForTest(t, cluster)
	ctx, cancel := context.WithTimeout(context.Background(), testFetchDeadline)
	defer cancel()

	err := recorder.Ready(ctx)
	if err == nil {
		t.Fatal("an unreadable min.insync.replicas must refuse rather than assume a default")
	}

	if !strings.Contains(err.Error(), "min.insync.replicas") {
		t.Fatalf("err = %v, want it to name the config it could not read", err)
	}
}

// serveMinInSyncReplicas makes the fake cluster report the given value for the
// audit topic, which is the one topic config this readiness check reads.
func serveMinInSyncReplicas(cluster *kfake.Cluster, value string) {
	cluster.ControlKey(
		kmsg.DescribeConfigs.Int16(),
		func(req kmsg.Request) (kmsg.Response, error, bool) {
			cluster.KeepControl()
			configReq, ok := req.(*kmsg.DescribeConfigsRequest)
			if !ok {
				return nil, nil, false
			}

			resp, ok := configReq.ResponseKind().(*kmsg.DescribeConfigsResponse)
			if !ok {
				return nil, nil, false
			}

			resource := kmsg.NewDescribeConfigsResponseResource()
			resource.ResourceType = kmsg.ConfigResourceTypeTopic
			resource.ResourceName = testKafkaTopic
			config := kmsg.NewDescribeConfigsResponseResourceConfig()
			config.Name = "min.insync.replicas"
			reported := value
			config.Value = &reported
			resource.Configs = append(resource.Configs, config)
			resp.Resources = append(resp.Resources, resource)

			return resp, nil, true
		},
	)
}
