package audit

import (
	"context"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"goodkind.io/tack/internal/telemetry"
)

// failureBackoffCap bounds the pause between polls while a partition keeps
// failing, so a ledger outage costs a few log lines a minute rather than a
// storm, while recovery is noticed within the cap.
const failureBackoffCap = 30 * time.Second

// failureBackoffDoublings caps how many times the pause doubles.
const failureBackoffDoublings = 8

// projectFetches projects every partition batch one poll returned, winds
// back the partitions whose batch failed, and commits the rest. It reports
// whether any batch failed.
//
// The poll advanced the client's fetch position past every record it
// returned, including the ones a failed batch never projected. Those
// partitions are wound back to the failed batch's first record before
// anything is committed, so the next poll re-fetches them and no later batch
// can commit an offset past records the ledger never saw. The seek runs
// between polls and before the commit, the two conditions the client's own
// documentation sets for a group consumer.
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
	// A commit lost to a rebalance only redelivers, and a redelivered record
	// is refused by the ledger's identity claim, so the group offset may lag
	// the ledger but never lead it.
	if len(committed) > 0 {
		if err := c.kclient.CommitRecords(ctx, committed...); err != nil {
			slog.ErrorContext(ctx, "audit.consumer.commit_failed", slog.String("err", err.Error()))
		}
	}
	return len(rewind) > 0
}

// backOffAfterFailures pauses before the next poll when this one failed a
// batch, doubling the pause per consecutive failing poll up to the cap, and
// resets on a clean poll. The pause ends early on stop or cancellation.
func (c *Consumer) backOffAfterFailures(ctx context.Context, failed bool) {
	if !failed {
		c.consecutiveFailures = 0
		return
	}
	c.consecutiveFailures++
	doublings := min(c.consecutiveFailures, failureBackoffDoublings)
	wait := min(c.cfg.PollInterval<<uint(doublings), failureBackoffCap)
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
