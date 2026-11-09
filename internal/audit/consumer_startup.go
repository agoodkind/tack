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

// auditTopicPartitions sets the audit topic's partition count on a fresh
// broker (TACK-305). It matches the shardOf width to support parallel
// processing. Different organizations can share partitions. Correctness does
// not depend on the count because the consumer recomputes each shard from
// the payload.
const auditTopicPartitions = 256

// pingYugabyteReadyCapWait caps the backoff between Yugabyte readiness pings.
const pingYugabyteReadyCapWait = 5 * time.Second

// pingYugabyteUntilReady retries the YugabyteDB ping until it succeeds or
// ctx ends. The delay grows linearly to a cap. On a fresh environment, the
// consumer can start before ops audit seed-roles creates the audit LOGIN
// roles, and the initial pings fail SASL authentication. Earlier consumers
// exited with status 1 and Docker repeatedly restarted them (TACK-301).
// Retry attempts log at Warn. Cancellation logs at Error.
func pingYugabyteUntilReady(ctx context.Context, pool *pgxpool.Pool) error {
	attempts := 0
	for {
		attempts++
		err := pool.Ping(ctx)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			slog.ErrorContext(ctx, "audit.consumer.yugabyte_ping_failed", slog.String("err", err.Error()))
			return fmt.Errorf("audit consumer yugabyte ping: %w", err)
		}
		wait := min(time.Duration(attempts)*time.Second, pingYugabyteReadyCapWait)
		slog.WarnContext(ctx, "audit.consumer.yugabyte_not_ready",
			slog.Int("attempt", attempts),
			slog.String("err", err.Error()),
		)
		select {
		case <-ctx.Done():
			slog.ErrorContext(ctx, "audit.consumer.yugabyte_ping_failed", slog.String("err", ctx.Err().Error()))
			return fmt.Errorf("audit consumer yugabyte ping: %w", ctx.Err())
		case <-time.After(wait):
		}
	}
	if attempts > 1 {
		slog.InfoContext(ctx, "audit.consumer.yugabyte_ready", slog.Int("attempts", attempts))
	}
	return nil
}

// retentionConfigKey selects the broker's time-based record retention
// setting, which also applies to records that the consumer has not read.
const retentionConfigKey = "retention.ms"

// ensureAuditTopic creates a missing audit topic with auditTopicPartitions
// partitions (TACK-305). It uses the broker's default replication factor
// (-1) for both single-broker and multi-broker deployments. An existing
// topic keeps its partitions, and the consumer attempts to update retention
// to the configured duration.
//
// Topic retention provides time to process events after a consumer outage.
// The seven-day broker default discarded the 2026-07-06 to 07-21 events
// during an outage (TACK-336).
func ensureAuditTopic(ctx context.Context, client *kgo.Client, topic string, retention time.Duration) error {
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
		slog.ErrorContext(ctx, "audit.consumer.topic_create_request_failed",
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
			// An access-control rule or an older broker version can reject the retention
			// update. The consumer logs the error and continues processing with the
			// existing retention duration.
			if err := setTopicRetention(ctx, client, topic, retentionMs); err != nil {
				slog.ErrorContext(ctx, "audit.consumer.topic_retention_unchanged",
					slog.String("topic", topic), slog.String("err", err.Error()))
			}
			continue
		}
		slog.ErrorContext(ctx, "audit.consumer.topic_create_failed",
			slog.String("topic", topic),
			slog.String("err", codeErr.Error()),
		)
		return fmt.Errorf("audit consumer create topic %s: %w", topic, codeErr)
	}
	slog.InfoContext(ctx, "audit.consumer.topic_ensured",
		slog.String("topic", topic),
		slog.Int("partitions", auditTopicPartitions),
		slog.String("retention", retention.String()),
	)
	return nil
}

// setTopicRetention updates retention.ms on an existing topic with one
// incremental configuration change. It preserves the other topic settings.
func setTopicRetention(ctx context.Context, client *kgo.Client, topic string, retentionMs string) error {
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
		slog.ErrorContext(ctx, "audit.consumer.topic_retention_request_failed",
			slog.String("topic", topic), slog.String("err", err.Error()))
		return fmt.Errorf("audit consumer set retention on %s: %w", topic, err)
	}
	for _, respResource := range resp.Resources {
		if respResource.ErrorCode == 0 {
			continue
		}
		codeErr := kerr.ErrorForCode(respResource.ErrorCode)
		slog.ErrorContext(ctx, "audit.consumer.topic_retention_failed",
			slog.String("topic", topic), slog.String("err", codeErr.Error()))
		return fmt.Errorf("audit consumer set retention on %s: %w", topic, codeErr)
	}
	slog.InfoContext(ctx, "audit.consumer.topic_retention_set",
		slog.String("topic", topic), slog.String("retention_ms", retentionMs))
	return nil
}
