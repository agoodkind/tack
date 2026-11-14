package audit

import (
	"context"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// TestReadyRejectsLeaderlessPartition checks every partition, not only partition zero.
func TestReadyRejectsLeaderlessPartition(t *testing.T) {
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

// TestReadyRefusesAPartitionlessTopic checks rejection of an empty partition list.
func TestReadyRefusesAPartitionlessTopic(t *testing.T) {
	t.Parallel()
	cluster := newFakeCluster(t)
	serveMetadata(cluster)
	recorder := newKafkaRecorderForTest(t, cluster)
	ctx, cancel := context.WithTimeout(context.Background(), testFetchDeadline)
	defer cancel()

	err := recorder.Ready(ctx)
	if err == nil {
		t.Fatal("a topic without partitions must refuse readiness")
	}

	if !strings.Contains(err.Error(), "no partitions") {
		t.Fatalf("err = %v, want rejection of the empty partition list", err)
	}
}

// TestReadyAcceptsAFullyLedTopic checks successful readiness with a leader on
// each partition as a control for the rejection cases.
func TestReadyAcceptsAFullyLedTopic(t *testing.T) {
	t.Parallel()
	cluster := newFakeCluster(t)
	serveMetadata(cluster, metadataPartition(0, 1), metadataPartition(1, 1))
	recorder := newKafkaRecorderForTest(t, cluster)
	ctx, cancel := context.WithTimeout(context.Background(), testFetchDeadline)
	defer cancel()

	if err := recorder.Ready(ctx); err != nil {
		t.Fatalf("a topic with leaders on all partitions must be ready: %v", err)
	}
}

// serveMetadata supplies the requested partition metadata directly because
// kfake does not create leaderless partitions through its normal broker operations.
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

// TestReadyRejectsEmptyInSyncReplicaSet checks that a leader alone does not
// satisfy the producer's acks=all requirements.
func TestReadyRejectsEmptyInSyncReplicaSet(t *testing.T) {
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
			"a partition without in-sync replicas must refuse acks=all writes",
		)
	}

	if !strings.Contains(err.Error(), "0 in-sync replicas") {
		t.Fatalf("err = %v, want it to name the empty in-sync set", err)
	}
}

// TestReadyRefusesAPartitionBelowMinInSyncReplicas checks rejection when the
// topic requires more in-sync replicas than the partition reports.
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

// TestReadyRefusesAnUnreadableMinInSyncReplicas checks refusal rather than an
// assumed default when the effective topic minimum cannot be read.
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

// serveMinInSyncReplicas supplies the effective audit-topic minimum.
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
