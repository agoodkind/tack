package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

// A reassignment runs in the background for as long as the copying takes.
// `ops queue replication-progress` answers how much of it is left.

// RunQueueReplicationProgress reports how many partitions the controller is
// still moving and how many already hold the target number of copies.
func RunQueueReplicationProgress(
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

	moving, err := readQueueMovingPartitions(ctx, client, topic)
	if err != nil {
		return err
	}
	partitions, err := readQueueTopicPartitions(ctx, client, topic)
	if err != nil {
		return err
	}
	targetReplicaCount := 0
	for _, partition := range partitions {
		if len(partition.Replicas) > targetReplicaCount {
			targetReplicaCount = len(partition.Replicas)
		}
	}
	settled := 0
	for _, partition := range partitions {
		if moving[partition.Partition] {
			continue
		}
		if len(partition.Replicas) == targetReplicaCount {
			settled++
		}
	}
	slog.InfoContext(
		ctx, "ops.queue.replication_progress",
		slog.String("topic", topic),
		slog.Int("moving", len(moving)),
		slog.Int("settled", settled),
	)
	report := "topic " + topic + "\n" +
		"  partitions: " + strconv.Itoa(len(partitions)) + "\n" +
		"  target copies per partition: " + strconv.Itoa(targetReplicaCount) + "\n" +
		"  still moving: " + strconv.Itoa(len(moving)) + "\n" +
		"  at the target copy count: " + strconv.Itoa(settled)
	return writeQueueOutput(ctx, sink, report)
}

// readQueueMovingPartitions returns the partitions the controller has not
// finished reassigning, keyed by partition number.
func readQueueMovingPartitions(
	ctx context.Context,
	client *kgo.Client,
	topic string,
) (map[int32]bool, error) {
	req := kmsg.NewPtrListPartitionReassignmentsRequest()
	req.TimeoutMillis = queueRequestTimeoutMillis
	reqTopic := kmsg.NewListPartitionReassignmentsRequestTopic()
	reqTopic.Topic = topic
	req.Topics = append(req.Topics, reqTopic)

	callCtx, cancel := context.WithTimeout(ctx, queueRequestTimeout)
	defer cancel()
	resp, err := req.RequestWith(callCtx, client)
	if err != nil {
		slog.ErrorContext(ctx, "ops.queue.list_reassignments_failed",
			slog.String("topic", topic), slog.String("err", err.Error()))
		return nil, fmt.Errorf("ops queue list reassignments of %s: %w", topic, err)
	}
	if resp.ErrorCode != 0 {
		codeErr := kerr.ErrorForCode(resp.ErrorCode)
		slog.ErrorContext(ctx, "ops.queue.list_reassignments_rejected",
			slog.String("topic", topic), slog.String("err", codeErr.Error()))
		return nil, fmt.Errorf("ops queue reassignment listing for %s was refused: %w", topic, codeErr)
	}
	moving := map[int32]bool{}
	for _, respTopic := range resp.Topics {
		for _, partition := range respTopic.Partitions {
			if len(partition.AddingReplicas) > 0 || len(partition.RemovingReplicas) > 0 {
				moving[partition.Partition] = true
			}
		}
	}
	return moving, nil
}
