package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/clock"
)

const (
	// assemblyCompletionBatch limits the number of ready assemblies that each
	// completion scan selects.
	assemblyCompletionBatch = 32
	// assemblyWaitingAlertAge sets the age at which an assembly with missing
	// parts contributes to the waiting count.
	assemblyWaitingAlertAge = 5 * time.Minute
	// assemblyAlertInterval sets the minimum time between assembly alert checks.
	assemblyAlertInterval = time.Minute
	// assemblyChainRetries limits completion attempts when chain conflicts occur.
	assemblyChainRetries = 8
)

// completeAssemblies selects ready assemblies without an invalid reason after
// each poll. It completes each event in a separate transaction and collects
// the projected events for one ClickHouse batch write after the loop.
func (c *Consumer) completeAssemblies(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	rows, err := c.ybpool.Query(ctx, `
		SELECT event_id FROM audit.event_assemblies
		 WHERE ready AND invalid_reason IS NULL
		 ORDER BY created_at ASC, event_id ASC
		 LIMIT $1
	`, assemblyCompletionBatch)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.assembly_scan_failed", slog.String("err", err.Error()))
		return
	}
	eventIDs, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.assembly_scan_failed", slog.String("err", err.Error()))
		return
	}
	completed := make([]projectedEvent, 0, len(eventIDs))
	for _, eventID := range eventIDs {
		if projected, landed := c.completeAssemblyWithRetry(ctx, eventID); landed {
			completed = append(completed, projected)
		}
	}
	c.writeClickHouseBatch(ctx, completed)
}

// completeAssemblyWithRetry retries chain conflicts within the attempt limit.
// It returns the projected event and reports whether it produced a ledger row.
// It records verification and event validation errors as invalid_reason and
// logs other failures for another poll.
func (c *Consumer) completeAssemblyWithRetry(ctx context.Context, eventID uuid.UUID) (projectedEvent, bool) {
	var none projectedEvent
	for attempt := 1; ; attempt++ {
		projected, landed, err := c.completeAssembly(ctx, eventID)
		switch {
		case err == nil:
			return projected, landed
		case attempt < assemblyChainRetries && isRetryableChainErr(err):
			slog.WarnContext(ctx, "audit.consumer.assembly_chain_retry", slog.String("event_id", eventID.String()), slog.Int("attempt", attempt), slog.String("err", err.Error()))
			continue
		case errors.Is(err, errMalformedPayload):
			if _, markErr := c.ybpool.Exec(ctx, `
				UPDATE audit.event_assemblies SET invalid_reason = $2 WHERE event_id = $1
			`, eventID, err.Error()); markErr != nil {
				slog.ErrorContext(ctx, "audit.consumer.assembly_mark_invalid_failed", slog.String("event_id", eventID.String()), slog.String("err", markErr.Error()))
			}
			slog.ErrorContext(ctx, "audit.consumer.assembly_invalid", slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
			return none, false
		default:
			slog.ErrorContext(ctx, "audit.consumer.assembly_complete_failed", slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
			return none, false
		}
	}
}

// completeAssembly locks a ready assembly, streams and verifies its parts,
// writes its read manifest parts, fills a missing commit version, appends the
// event, and finishes the assembly in one transaction. A crash before commit leaves the assembly ready for another poll.
func (c *Consumer) completeAssembly(ctx context.Context, eventID uuid.UUID) (projectedEvent, bool, error) {
	tx, err := c.ybpool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.begin_failed", slog.String("err", err.Error()))
		return projectedEvent{}, false, fmt.Errorf("begin audit event %s completion: %w", eventID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var header []byte
	var commitVersion string
	err = tx.QueryRow(ctx, `
		SELECT header, commit_version FROM audit.event_assemblies
		 WHERE event_id = $1 AND ready AND invalid_reason IS NULL
		 FOR UPDATE
	`, eventID).Scan(&header, &commitVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		var completed projectedEvent
		return completed, false, nil
	}
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.assembly_read_failed", slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
		return projectedEvent{}, false, fmt.Errorf("read audit event %s assembly: %w", eventID, err)
	}
	event, transportDigest, err := streamAssembly(ctx, tx, eventID, header)
	if err != nil {
		return projectedEvent{}, false, err
	}
	if event.Context.CommitVersion == "" {
		event.Context.CommitVersion = commitVersion
	}
	projected, landed, err := c.appendAssembledEvent(ctx, tx, event)
	if err != nil {
		return projectedEvent{}, false, err
	}
	if err := finishAssembly(ctx, tx, eventID, transportDigest); err != nil {
		return projectedEvent{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.commit_failed", slog.String("err", err.Error()))
		return projectedEvent{}, false, fmt.Errorf("commit audit event %s completion: %w", eventID, err)
	}
	return projected, landed, nil
}

// alertWaitingAssemblies checks at most once per alert interval and logs
// counts of overdue assemblies with missing parts and invalid assemblies.
// It does not delete assemblies with missing parts.
func (c *Consumer) alertWaitingAssemblies(ctx context.Context) {
	now := clock.Now()
	if now.Sub(c.assemblyAlertAt) < assemblyAlertInterval {
		return
	}
	c.assemblyAlertAt = now
	var waiting, invalid int64
	if err := c.ybpool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE NOT ready AND created_at < now() - make_interval(secs => $1)),
		       count(*) FILTER (WHERE invalid_reason IS NOT NULL)
		  FROM audit.event_assemblies
	`, assemblyWaitingAlertAge.Seconds()).Scan(&waiting, &invalid); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.assembly_alert_failed", slog.String("err", err.Error()))
		return
	}
	if waiting > 0 || invalid > 0 {
		slog.ErrorContext(ctx, "audit.consumer.assemblies_incomplete",
			slog.Int64("waiting", waiting), slog.Int64("invalid", invalid),
			slog.String("err", "audit event assemblies have missing parts or validation failures"))
	}
}
