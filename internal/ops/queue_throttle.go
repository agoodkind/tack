package ops

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

// A reassignment copies whole partitions between brokers. Left unbounded it
// saturates the link the live audit traffic also uses, and a throttle caps the
// bytes per second each broker spends on that copying.

const (
	// queueLeaderThrottleRateKey caps what a broker sends as a partition
	// leader during a reassignment. It is a per-broker setting.
	queueLeaderThrottleRateKey = "leader.replication.throttled.rate"
	// queueFollowerThrottleRateKey caps what a broker fetches as a follower
	// during a reassignment. It is a per-broker setting.
	queueFollowerThrottleRateKey = "follower.replication.throttled.rate"
	// queueLeaderThrottleReplicasKey lists the topic's leader replicas the cap
	// applies to.
	queueLeaderThrottleReplicasKey = "leader.replication.throttled.replicas"
	// queueFollowerThrottleReplicasKey lists the topic's follower replicas the
	// cap applies to.
	queueFollowerThrottleReplicasKey = "follower.replication.throttled.replicas"
	// queueEveryReplica is the wildcard both replica lists take.
	queueEveryReplica = "*"
)

// setQueueThrottle caps the bytes each broker spends copying partitions and
// points both replica lists at every partition of the topic.
func setQueueThrottle(
	ctx context.Context,
	client *kgo.Client,
	topic string,
	brokerIDs []int32,
	throttleBytes int64,
) error {
	rate := strconv.FormatInt(throttleBytes, 10)
	rateEdits := []queueConfigEdit{
		{Name: queueLeaderThrottleRateKey, Value: rate},
		{Name: queueFollowerThrottleRateKey, Value: rate},
	}
	for _, brokerIdentifier := range brokerIDs {
		name := strconv.Itoa(int(brokerIdentifier))
		if err := applyQueueConfigs(ctx, client, kmsg.ConfigResourceTypeBroker, name,
			kmsg.IncrementalAlterConfigOpSet, rateEdits); err != nil {
			return err
		}
	}
	replicaEdits := []queueConfigEdit{
		{Name: queueLeaderThrottleReplicasKey, Value: queueEveryReplica},
		{Name: queueFollowerThrottleReplicasKey, Value: queueEveryReplica},
	}
	if err := applyQueueConfigs(ctx, client, kmsg.ConfigResourceTypeTopic, topic,
		kmsg.IncrementalAlterConfigOpSet, replicaEdits); err != nil {
		return err
	}
	slog.InfoContext(
		ctx, "ops.queue.throttle_set",
		slog.String("topic", topic),
		slog.Int64("bytes_per_second", throttleBytes),
		slog.Int("broker_count", len(brokerIDs)),
	)
	return nil
}

// RunQueueClearThrottle removes the four throttle keys a reassignment set. A
// cap left behind after the move keeps slowing the recovery of any replica that
// falls behind later.
func RunQueueClearThrottle(
	ctx context.Context,
	cfg *config.Config,
	topic string,
	sink clispec.ResultSink,
) error {
	client, err := openQueueClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	brokers, err := readQueueBrokers(ctx, client)
	if err != nil {
		return err
	}
	rateEdits := []queueConfigEdit{
		{Name: queueLeaderThrottleRateKey, Value: ""},
		{Name: queueFollowerThrottleRateKey, Value: ""},
	}
	for _, broker := range brokers {
		name := strconv.Itoa(int(broker.NodeID))
		if err := applyQueueConfigs(ctx, client, kmsg.ConfigResourceTypeBroker, name,
			kmsg.IncrementalAlterConfigOpDelete, rateEdits); err != nil {
			return err
		}
	}
	replicaEdits := []queueConfigEdit{
		{Name: queueLeaderThrottleReplicasKey, Value: ""},
		{Name: queueFollowerThrottleReplicasKey, Value: ""},
	}
	if err := applyQueueConfigs(ctx, client, kmsg.ConfigResourceTypeTopic, topic,
		kmsg.IncrementalAlterConfigOpDelete, replicaEdits); err != nil {
		return err
	}
	slog.InfoContext(ctx, "ops.queue.throttle_cleared",
		slog.String("topic", topic), slog.Int("broker_count", len(brokers)))
	return writeQueueOutput(ctx, sink,
		"throttle keys removed from topic "+topic+" and from "+strconv.Itoa(len(brokers))+" brokers")
}
