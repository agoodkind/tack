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
// each poll. It completes each event in a separate transaction.
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
	for _, eventID := range eventIDs {
		c.completeAssemblyWithRetry(ctx, eventID)
	}
}

// completeAssemblyWithRetry retries chain conflicts within the attempt limit.
// It records verification and event validation errors as invalid_reason and
// logs other failures for another poll.
func (c *Consumer) completeAssemblyWithRetry(ctx context.Context, eventID uuid.UUID) {
	for attempt := 1; ; attempt++ {
		err := c.completeAssembly(ctx, eventID)
		switch {
		case err == nil:
			return
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
			return
		default:
			slog.ErrorContext(ctx, "audit.consumer.assembly_complete_failed", slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
			return
		}
	}
}

// completeAssembly locks a ready assembly, streams and verifies its parts,
// writes its read manifest parts, fills a missing commit version, appends the
// event, and finishes the assembly in one transaction. A crash before commit leaves the assembly ready for another poll.
func (c *Consumer) completeAssembly(ctx context.Context, eventID uuid.UUID) error {
	tx, err := c.ybpool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.begin_failed", slog.String("err", err.Error()))
		return fmt.Errorf("begin audit event %s completion: %w", eventID, err)
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
		return nil
	}
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.assembly_read_failed", slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("read audit event %s assembly: %w", eventID, err)
	}
	event, transportDigest, err := streamAssembly(ctx, tx, eventID, header)
	if err != nil {
		return err
	}
	if event.Context.CommitVersion == "" {
		event.Context.CommitVersion = commitVersion
	}
	if err := c.appendAssembledEvent(ctx, tx, event); err != nil {
		return err
	}
	if err := finishAssembly(ctx, tx, eventID, transportDigest); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.commit_failed", slog.String("err", err.Error()))
		return fmt.Errorf("commit audit event %s completion: %w", eventID, err)
	}
	return nil
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
