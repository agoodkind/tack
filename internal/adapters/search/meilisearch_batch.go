package search

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/meilisearch/meilisearch-go"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// indexTaskPollInterval is how often IndexBatch polls Meilisearch for the
// outcome of its indexing task.
const indexTaskPollInterval = 250 * time.Millisecond

// IndexBatch adds or replaces docs in one task and waits for the task, so a
// backfill reports rejected documents instead of dropping them silently.
func (c *Client) IndexBatch(ctx context.Context, collection string, docs []*domainsearch.NodeDoc) error {
	if len(docs) == 0 {
		return nil
	}
	primaryKey := "id"
	task, err := c.meili.Index(collection).AddDocumentsWithContext(ctx, docs, &meilisearch.DocumentOptions{PrimaryKey: &primaryKey})
	if err != nil {
		slog.WarnContext(ctx, "search.batch_index_failed", slog.String("collection", collection), slog.String("err", err.Error()))
		return fmt.Errorf("index batch of %d in %s: %w", len(docs), collection, err)
	}
	finished, err := c.meili.WaitForTaskWithContext(ctx, task.TaskUID, indexTaskPollInterval)
	if err != nil {
		slog.WarnContext(ctx, "search.batch_wait_failed", slog.Int64("task_uid", task.TaskUID), slog.String("err", err.Error()))
		return fmt.Errorf("wait for index task %d: %w", task.TaskUID, err)
	}
	if finished.Status != meilisearch.TaskStatusSucceeded {
		slog.WarnContext(ctx, "search.batch_task_failed", slog.Int64("task_uid", task.TaskUID), slog.String("status", string(finished.Status)))
		return fmt.Errorf("index task %d ended %s: %s", task.TaskUID, finished.Status, finished.Error.Message)
	}
	telemetry.L(ctx).Info("search.batch_indexed", slog.String("collection", collection), slog.Int("count", len(docs)))
	return nil
}
