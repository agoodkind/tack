package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/twmb/franz-go/pkg/kmsg"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

// The bodies behind `ops queue status` and `ops queue set-min-insync`. Each one
// prints what the cluster answered, in the words the cluster used.

// RunQueueStatus prints the criterion-10 reading for the audit queue: the live
// brokers with their advertised addresses, the controller quorum, and the copy
// counts on both the audit topic and the consumer-position topic.
func RunQueueStatus(ctx context.Context, cfg *config.Config, sink clispec.ResultSink) error {
	client, err := openQueueClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	brokers, err := readQueueBrokers(ctx, client)
	if err != nil {
		return err
	}
	quorum, err := readQueueQuorum(ctx, client)
	if err != nil {
		return err
	}
	auditShape, err := readQueueTopicShape(ctx, client, cfg.AuditKafkaTopic)
	if err != nil {
		return err
	}
	positionShape, err := readQueueTopicShape(ctx, client, queueConsumerPositionTopic)
	if err != nil {
		return err
	}
	slog.InfoContext(
		ctx, "ops.queue.status",
		slog.Int("broker_count", len(brokers)),
		slog.Int("quorum_voter_count", len(quorum.VoterIDs)),
	)
	report := renderQueueStatus(brokers, quorum, []queueTopicShape{auditShape, positionShape})
	return writeQueueOutput(ctx, sink, report)
}

// renderQueueStatus lays the cluster's answers out one fact per line.
func renderQueueStatus(brokers []queueBroker, quorum queueQuorum, shapes []queueTopicShape) string {
	var report strings.Builder
	report.WriteString("brokers: " + strconv.Itoa(len(brokers)) + "\n")
	for _, broker := range brokers {
		report.WriteString("  broker " + strconv.Itoa(int(broker.NodeID)) + " advertises " + broker.Address + "\n")
	}
	report.WriteString("controller quorum leader: " + strconv.Itoa(int(quorum.LeaderID)) + "\n")
	report.WriteString("controller quorum voters: " + joinBrokerIDs(quorum.VoterIDs) + "\n")
	for _, shape := range shapes {
		report.WriteString("topic " + shape.Topic + "\n")
		report.WriteString("  partitions: " + strconv.Itoa(shape.PartitionCount) + "\n")
		report.WriteString("  smallest replica count: " + strconv.Itoa(shape.SmallestReplicaCount) + "\n")
		report.WriteString("  largest replica count: " + strconv.Itoa(shape.LargestReplicaCount) + "\n")
		report.WriteString("  smallest in-sync count: " + strconv.Itoa(shape.SmallestInSyncCount) + "\n")
		report.WriteString("  " + queueMinInSyncKey + ": " + strconv.Itoa(shape.MinInSyncReplicas) + "\n")
	}
	return strings.TrimRight(report.String(), "\n")
}

// joinBrokerIDs renders a broker id list as one comma-separated field.
func joinBrokerIDs(identifiers []int32) string {
	if len(identifiers) == 0 {
		return "none"
	}
	rendered := make([]string, 0, len(identifiers))
	for _, brokerIdentifier := range identifiers {
		rendered = append(rendered, strconv.Itoa(int(brokerIdentifier)))
	}
	return strings.Join(rendered, ",")
}

// RunQueueSetMinInsync writes min.insync.replicas onto a topic. A topic holding
// fewer copies than its minimum rejects every acks=all write with
// NOT_ENOUGH_REPLICAS. The command refuses that order and reports both counts.
func RunQueueSetMinInsync(
	ctx context.Context,
	cfg *config.Config,
	topic string,
	replicas int,
	sink clispec.ResultSink,
) error {
	if replicas <= 0 {
		return errQueueNoReplicas
	}
	client, err := openQueueClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	shape, err := readQueueTopicShape(ctx, client, topic)
	if err != nil {
		return err
	}
	if shape.SmallestReplicaCount < replicas {
		thin := fmt.Errorf(
			"ops queue set-min-insync: topic %s keeps %d copies of its thinnest partition, below the requested minimum of %d",
			topic, shape.SmallestReplicaCount, replicas,
		)
		slog.ErrorContext(ctx, "ops.queue.min_insync_rejected", slog.String("err", thin.Error()))
		return thin
	}
	edits := []queueConfigEdit{{Name: queueMinInSyncKey, Value: strconv.Itoa(replicas)}}
	if err := applyQueueConfigs(ctx, client, kmsg.ConfigResourceTypeTopic, topic,
		kmsg.IncrementalAlterConfigOpSet, edits); err != nil {
		return err
	}
	slog.InfoContext(ctx, "ops.queue.min_insync_set",
		slog.String("topic", topic), slog.Int("replicas", replicas))
	return writeQueueOutput(ctx, sink,
		"topic "+topic+" now requires "+strconv.Itoa(replicas)+" in-sync copies per acknowledged write")
}

// writeQueueOutput prints what the cluster answered. A failed write is logged
// and returned: an operator running a migration step decides the next one from
// these lines.
func writeQueueOutput(ctx context.Context, sink clispec.ResultSink, report string) error {
	if err := sink.WriteText(ctx, report); err != nil {
		slog.ErrorContext(ctx, "ops.queue.write_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the queue report: %w", err)
	}
	return nil
}
