package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"goodkind.io/tack/internal/telemetry"
)

// failureBackoffCap limits the delay between failed polls to reduce repeated
// outage logs. The consumer retries after at most this delay.
const failureBackoffCap = 30 * time.Second

// failureBackoffDoublings caps how many times the pause doubles.
const failureBackoffDoublings = 8

// projectFetches processes each partition batch from one poll, resets failed
// partitions to their first record, and commits the successful partitions.
// It reports whether any batch failed.
//
// Polling advances the client's fetch position even for records that fail
// projection. This call resets failed partitions between polls and before
// its explicit commits to retry records that the failed batch did not store.
// The client also recommends blocking concurrent group revocation during a
// reset.
func (c *Consumer) projectFetches(ctx context.Context, fetches kgo.Fetches) bool {
	batches := groupByPartition(fetches)
	committed := make([]*kgo.Record, 0, len(batches))
	rewind := map[string]map[int32]kgo.EpochOffset{}
	for tp, records := range batches {
		if err := c.projectBatch(ctx, tp, records); err != nil {
			telemetry.IncAuditConsumerProcessed("error", int64(len(records)))
			slog.ErrorContext(ctx, "audit.consumer.project_failed",
				slog.String("topic", tp.Topic),
				slog.Int("partition", int(tp.Partition)),
				slog.String("err", err.Error()),
			)
			first := records[0]
			if rewind[tp.Topic] == nil {
				rewind[tp.Topic] = map[int32]kgo.EpochOffset{}
			}
			rewind[tp.Topic][tp.Partition] = kgo.EpochOffset{Epoch: first.LeaderEpoch, Offset: first.Offset}
			continue
		}
		last := records[len(records)-1]
		committed = append(committed, last)
		telemetry.IncAuditConsumerProcessed("ok", int64(len(records)))
		telemetry.SetAuditConsumerOffsetCommitted(tp.Topic, tp.Partition, last.Offset+1)
		c.recordSummary(ctx, records, last.Offset+1)
	}
	if len(rewind) > 0 {
		c.kclient.SetOffsets(rewind)
	}
	// This call commits Kafka offsets for successful batches after their
	// YugabyteDB transactions commit. A failed Kafka commit during rebalance can
	// cause redelivery. The ledger's event identity claim rejects repeat inserts.
	if len(committed) > 0 {
		if err := c.kclient.CommitRecords(ctx, committed...); err != nil {
			slog.ErrorContext(ctx, "audit.consumer.commit_failed", slog.String("err", err.Error()))
		}
	}
	return len(rewind) > 0
}

// backOffAfterFailures pauses after a poll fails a batch. Each consecutive
// failed poll with records doubles the pause up to the cap. A successful
// poll with records resets the failure count. Stop or cancellation ends the
// pause early.
func (c *Consumer) backOffAfterFailures(ctx context.Context, failed bool) {
	if !failed {
		c.consecutiveFailures = 0
		return
	}
	c.consecutiveFailures++
	// The initial delay and every doubled delay stay within the cap and the
	// duration type's range.
	wait := min(c.cfg.PollInterval, failureBackoffCap)
	for i := 0; i < c.consecutiveFailures && i < failureBackoffDoublings && wait < failureBackoffCap; i++ {
		wait = min(wait*2, failureBackoffCap)
	}
	slog.WarnContext(ctx, "audit.consumer.retry_backoff",
		slog.Int("consecutive_failures", c.consecutiveFailures),
		slog.String("wait", wait.String()),
	)
	select {
	case <-c.stop:
	case <-ctx.Done():
	case <-time.After(wait):
	}
}
