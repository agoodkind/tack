package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"goodkind.io/tack/internal/telemetry"
)

// failureBackoffCap bounds the poll delay during partition failures and the
// delay before detecting recovery.
const failureBackoffCap = 30 * time.Second

// failureBackoffDoublings caps how many times the pause doubles.
const failureBackoffDoublings = 8

// projectFetches projects each partition batch and reports whether any failed.
// It resets failed partitions to their first fetched record before committing
// successful batches. The next poll retries records the failed batch did not
// project. Offsets must not commit past unrecorded events. SetOffsets must run
// between polls and before the commit, as required for a group consumer.
func (c *Consumer) projectFetches(ctx context.Context, fetches kgo.Fetches) bool {
	batches := groupByPartition(fetches)
	committed := make([]*kgo.Record, 0, len(batches))
	rewind := map[string]map[int32]kgo.EpochOffset{}

	for tp, records := range batches {
		if err := c.projectBatch(ctx, tp, records); err != nil {
			telemetry.IncAuditConsumerProcessed("error", int64(len(records)))
			slog.ErrorContext(
				ctx,
				"audit.consumer.project_failed",
				slog.String("topic", tp.Topic),
				slog.Int("partition", int(tp.Partition)),
				slog.String("err", err.Error()),
			)
			first := records[0]
			if rewind[tp.Topic] == nil {
				rewind[tp.Topic] = map[int32]kgo.EpochOffset{}
			}

			rewind[tp.Topic][tp.Partition] = kgo.EpochOffset{
				Epoch:  first.LeaderEpoch,
				Offset: first.Offset,
			}
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

	// A rebalance can prevent an offset commit and cause redelivery. The
	// ledger's identity claim rejects duplicates. The group offset can lag
	// the ledger but must not advance ahead of committed events.
	if len(committed) > 0 {
		if err := c.kclient.CommitRecords(ctx, committed...); err != nil {
			slog.ErrorContext(ctx, "audit.consumer.commit_failed", slog.String("err", err.Error()))
		}
	}

	return len(rewind) > 0
}

// backOffAfterFailures doubles the pause for consecutive failed polls up to
// the cap and resets it after a successful poll. Stop or cancellation ends
// the pause early.
func (c *Consumer) backOffAfterFailures(ctx context.Context, failed bool) {
	if !failed {
		c.consecutiveFailures = 0
		return
	}

	c.consecutiveFailures++
	// Cap the initial wait and every doubling to prevent duration overflow.
	wait := min(c.cfg.PollInterval, failureBackoffCap)
	for i := 0; i < c.consecutiveFailures && i < failureBackoffDoublings && wait < failureBackoffCap; i++ {
		wait = min(wait*2, failureBackoffCap)
	}

	slog.WarnContext(
		ctx,
		"audit.consumer.retry_backoff",
		slog.Int("consecutive_failures", c.consecutiveFailures),
		slog.String("wait", wait.String()),
	)
	select {
	case <-c.stop:
	case <-ctx.Done():
	case <-time.After(wait):
	}
}
