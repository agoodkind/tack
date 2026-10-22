package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// appendAssembledEvent prepares the event and writes actor personal data,
// the permanent event identity claim, and the ledger row in a savepoint.
// It accepts an already projected event without another row. After a new
// append, it stores read manifest parts and records token use.
func (c *Consumer) appendAssembledEvent(ctx context.Context, tx pgx.Tx, event Event, targets []Target) (projectedEvent, bool, error) {
	prepared, err := prepareEvent(ctx, event)
	if err != nil {
		return projectedEvent{}, false, err
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.savepoint_failed", slog.String("err", err.Error()))
		return projectedEvent{}, false, fmt.Errorf("create savepoint for audit event %s: %w", event.EventID, err)
	}
	piiRef, err := writeEventPII(ctx, savepoint, prepared.Event)
	if err != nil {
		_ = savepoint.Rollback(ctx)
		return projectedEvent{}, false, err
	}
	prepared.PIIRef = piiRef
	projected, err := c.advanceChain(ctx, savepoint, prepared)
	if errors.Is(err, errAlreadyProjected) {
		_ = savepoint.Rollback(ctx)
		slog.WarnContext(ctx, "audit.consumer.assembly_already_projected", slog.String("event_id", event.EventID.String()))
		return projected, false, nil
	}
	if err != nil {
		_ = savepoint.Rollback(ctx)
		return projectedEvent{}, false, err
	}
	if err := savepoint.Commit(ctx); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.savepoint_release_failed", slog.String("err", err.Error()))
		return projectedEvent{}, false, fmt.Errorf("release savepoint for audit event %s: %w", event.EventID, err)
	}
	if err := storeReadManifest(ctx, tx, event, targets); err != nil {
		return projectedEvent{}, false, err
	}
	if err := recordTokenUses(ctx, tx, []projectedEvent{projected}); err != nil {
		return projectedEvent{}, false, err
	}
	return projected, true, nil
}

// storeReadManifest stores permanent read target parts by organization, event,
// and index. It uses the JSON arrays that ReadManifestDigest hashes.
func storeReadManifest(ctx context.Context, tx pgx.Tx, event Event, targets []Target) error {
	if event.Context.Read == nil || event.Context.Read.ManifestParts == 0 {
		return nil
	}
	parts, err := readManifestParts(targets)
	if err != nil {
		return rejectTransport(ctx, event.EventID, err.Error())
	}
	var partIndex int64
	for _, payload := range parts {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit.event_parts (org_id, event_id, part_index, digest, payload)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (org_id, event_id, part_index) DO NOTHING
		`, event.Context.OrgID, event.EventID, partIndex, sha256Hex(payload), payload); err != nil {
			slog.ErrorContext(ctx, "audit.consumer.manifest_store_failed", slog.String("event_id", event.EventID.String()), slog.String("err", err.Error()))
			return fmt.Errorf("store audit event %s read manifest part %d: %w", event.EventID, partIndex, err)
		}
		partIndex++
	}
	return nil
}

// finishAssembly records the completed transport digest and deletes the
// transient parts and assembly row in the caller's transaction.
func finishAssembly(ctx context.Context, tx pgx.Tx, eventID uuid.UUID, transportDigest string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO audit.completed_transports (event_id, transport_digest)
		VALUES ($1, $2)
		ON CONFLICT (event_id) DO NOTHING
	`, eventID, transportDigest)
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM audit.event_assembly_parts WHERE event_id = $1`, eventID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `DELETE FROM audit.event_assemblies WHERE event_id = $1`, eventID)
	}
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.assembly_finish_failed", slog.String("event_id", eventID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("finish audit event %s assembly: %w", eventID, err)
	}
	return nil
}
