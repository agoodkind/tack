package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// auditTopicPartitions sets the fresh topic's partition count for parallel
// processing. The consumer derives chain shards from payloads independently
// of this throughput setting (TACK-305).
const auditTopicPartitions = 256

// pingYugabyteReadyCapWait caps the backoff between Yugabyte readiness pings.
const pingYugabyteReadyCapWait = 5 * time.Second

// pingYugabyteUntilReady retries with capped linear backoff until connection
// succeeds or ctx ends. Fresh deployments may start before ops audit seed-roles
// creates logins. The retry loop permits role initialization without crash
// loops; failed attempts log at Warn (TACK-301).
func pingYugabyteUntilReady(ctx context.Context, pool *pgxpool.Pool) error {
	attempts := 0

	for {
		attempts++
		err := pool.Ping(ctx)
		if err == nil {
			break
		}

		if ctx.Err() != nil {
			slog.ErrorContext(
				ctx,
				"audit.consumer.yugabyte_ping_failed",
				slog.String("err", err.Error()),
			)

			return fmt.Errorf("audit consumer yugabyte ping: %w", err)
		}

		wait := min(time.Duration(attempts)*time.Second, pingYugabyteReadyCapWait)
		slog.WarnContext(
			ctx,
			"audit.consumer.yugabyte_not_ready",
			slog.Int("attempt", attempts),
			slog.String("err", err.Error()),
		)
		select {
		case <-ctx.Done():
			slog.ErrorContext(
				ctx,
				"audit.consumer.yugabyte_ping_failed",
				slog.String("err", ctx.Err().Error()),
			)

			return fmt.Errorf("audit consumer yugabyte ping: %w", ctx.Err())
		case <-time.After(wait):
		}
	}

	if attempts > 1 {
		slog.InfoContext(ctx, "audit.consumer.yugabyte_ready", slog.Int("attempts", attempts))
	}

	return nil
}

// retentionConfigKey controls how long the broker retains records, including
// records the consumer has not committed.
const retentionConfigKey = "retention.ms"

// ensureAuditTopic creates a missing topic with auditTopicPartitions and the
// broker's default replication factor. Existing topics preserve partitions
// while applying configured retention. Retention must cover consumer outage
// recovery rather than expiring pending audit events (TACK-305, TACK-336).
func ensureAuditTopic(
	ctx context.Context,
	client *kgo.Client,
	topic string,
	retention time.Duration,
) error {
	retentionMs := strconv.FormatInt(retention.Milliseconds(), 10)
	req := kmsg.NewPtrCreateTopicsRequest()
	reqTopic := kmsg.NewCreateTopicsRequestTopic()
	reqTopic.Topic = topic
	reqTopic.NumPartitions = auditTopicPartitions
	reqTopic.ReplicationFactor = -1
	reqConfig := kmsg.NewCreateTopicsRequestTopicConfig()
	reqConfig.Name = retentionConfigKey
	reqConfig.Value = &retentionMs
	reqTopic.Configs = append(reqTopic.Configs, reqConfig)
	req.Topics = append(req.Topics, reqTopic)

	resp, err := req.RequestWith(ctx, client)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.consumer.topic_create_request_failed",
			slog.String("topic", topic),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("audit consumer create topic %s: %w", topic, err)
	}

	for _, respTopic := range resp.Topics {
		if respTopic.ErrorCode == 0 {
			continue
		}

		codeErr := kerr.ErrorForCode(respTopic.ErrorCode)
		if errors.Is(codeErr, kerr.TopicAlreadyExists) {
			// Log a refused retention update while continuing projection with
			// the existing broker setting. Do not claim the update succeeded.
			if err := setTopicRetention(ctx, client, topic, retentionMs); err != nil {
				slog.ErrorContext(
					ctx,
					"audit.consumer.topic_retention_unchanged",
					slog.String("topic", topic),
					slog.String("err", err.Error()),
				)
			}

			continue
		}

		slog.ErrorContext(
			ctx,
			"audit.consumer.topic_create_failed",
			slog.String("topic", topic),
			slog.String("err", codeErr.Error()),
		)

		return fmt.Errorf("audit consumer create topic %s: %w", topic, codeErr)
	}

	slog.InfoContext(
		ctx,
		"audit.consumer.topic_ensured",
		slog.String("topic", topic),
		slog.Int("partitions", auditTopicPartitions),
		slog.String("retention", retention.String()),
	)

	return nil
}

// setTopicRetention changes only retention.ms using an incremental config
// update.
func setTopicRetention(
	ctx context.Context,
	client *kgo.Client,
	topic string,
	retentionMs string,
) error {
	req := kmsg.NewPtrIncrementalAlterConfigsRequest()
	resource := kmsg.NewIncrementalAlterConfigsRequestResource()
	resource.ResourceType = kmsg.ConfigResourceTypeTopic
	resource.ResourceName = topic
	config := kmsg.NewIncrementalAlterConfigsRequestResourceConfig()
	config.Name = retentionConfigKey
	config.Op = kmsg.IncrementalAlterConfigOpSet
	config.Value = &retentionMs
	resource.Configs = append(resource.Configs, config)
	req.Resources = append(req.Resources, resource)

	resp, err := req.RequestWith(ctx, client)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.consumer.topic_retention_request_failed",
			slog.String("topic", topic),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("audit consumer set retention on %s: %w", topic, err)
	}

	for _, respResource := range resp.Resources {
		if respResource.ErrorCode == 0 {
			continue
		}

		codeErr := kerr.ErrorForCode(respResource.ErrorCode)
		slog.ErrorContext(
			ctx,
			"audit.consumer.topic_retention_failed",
			slog.String("topic", topic),
			slog.String("err", codeErr.Error()),
		)

		return fmt.Errorf("audit consumer set retention on %s: %w", topic, codeErr)
	}

	slog.InfoContext(
		ctx,
		"audit.consumer.topic_retention_set",
		slog.String("topic", topic),
		slog.String("retention_ms", retentionMs),
	)

	return nil
}
