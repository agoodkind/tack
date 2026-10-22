package audit

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// storeTransportMessage checks for a completed event, then stores the
// message in its assembly. It marks the assembly ready when the manifest
// and every part exist.
func storeTransportMessage(ctx context.Context, tx pgx.Tx, message transportMessage, raw []byte) error {
	completed, err := completedTransport(ctx, tx, message)
	if err != nil || completed {
		return err
	}
	if err := storeAssembly(ctx, tx, message); err != nil {
		return err
	}
	if message.Message == messageKindPart {
		err = storeAssemblyPart(ctx, tx, message)
	} else {
		err = storeAssemblyHeader(ctx, tx, message, raw)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE audit.event_assemblies AS assembly
		   SET ready = true
		 WHERE assembly.event_id = $1 AND NOT assembly.ready AND assembly.header IS NOT NULL
		   AND assembly.part_count = (
		       SELECT count(*) FROM audit.event_assembly_parts AS part WHERE part.event_id = assembly.event_id)
	`, message.EventID); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.assembly_ready_failed", slog.String("event_id", message.EventID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("mark audit event %s assembly ready: %w", message.EventID, err)
	}
	return nil
}

// completedTransport accepts a replay when its digest matches the completed
// transport. It rejects a conflicting digest or a projected event without
// a completed transport digest.
func completedTransport(ctx context.Context, tx pgx.Tx, message transportMessage) (bool, error) {
	var digest *string
	var projected bool
	if err := tx.QueryRow(ctx, `
		SELECT (SELECT transport_digest FROM audit.completed_transports WHERE event_id = $1),
		       EXISTS (SELECT 1 FROM audit.projected_events WHERE event_id = $1)
	`, message.EventID).Scan(&digest, &projected); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.completed_read_failed", slog.String("event_id", message.EventID.String()), slog.String("err", err.Error()))
		return false, fmt.Errorf("read audit event %s completed transport: %w", message.EventID, err)
	}
	if digest == nil {
		if projected {
			return false, rejectTransport(ctx, message.EventID, "exists without a completed transport digest")
		}
		return false, nil
	}
	if *digest != message.TransportDigest {
		return false, rejectTransport(ctx, message.EventID, "has a transport digest that conflicts with the completed event")
	}
	return true, nil
}

// storeAssembly creates or reads the assembly and rejects messages with a
// different organization, shard, part count, or transport digest.
func storeAssembly(ctx context.Context, tx pgx.Tx, message transportMessage) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit.event_assemblies (event_id, org_id, shard, part_count, transport_digest)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (event_id) DO NOTHING
	`, message.EventID, message.OrgID, message.Shard, int64(message.PartCount), message.TransportDigest); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.assembly_insert_failed", slog.String("event_id", message.EventID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("store audit event %s assembly: %w", message.EventID, err)
	}
	var stored transportMessage
	var partCount int64
	if err := tx.QueryRow(ctx, `
		SELECT org_id, shard, part_count, transport_digest FROM audit.event_assemblies WHERE event_id = $1
	`, message.EventID).Scan(&stored.OrgID, &stored.Shard, &partCount, &stored.TransportDigest); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.assembly_read_failed", slog.String("event_id", message.EventID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("read audit event %s assembly: %w", message.EventID, err)
	}
	if stored.OrgID != message.OrgID || stored.Shard != message.Shard || partCount != int64(message.PartCount) || stored.TransportDigest != message.TransportDigest {
		return rejectTransport(ctx, message.EventID, "has a message that conflicts with its assembly")
	}
	return nil
}

// storeAssemblyPart validates the part index, size, and payload digest before
// storing the part once. It accepts identical retries and rejects different
// bytes for an existing index.
func storeAssemblyPart(ctx context.Context, tx pgx.Tx, message transportMessage) error {
	part := partFromMessage(message)
	if part.Index >= part.Count || len(part.Payload) > EventPartMaxBytes || sha256Hex(part.Payload) != part.Digest {
		return rejectTransport(ctx, part.EventID, fmt.Sprintf("has part %d with an invalid index, size, or payload digest", part.Index))
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO audit.event_assembly_parts (event_id, part_index, digest, payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (event_id, part_index) DO NOTHING
	`, part.EventID, int64(part.Index), part.Digest, part.Payload)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.part_insert_failed", slog.String("event_id", part.EventID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("store audit event %s part %d: %w", part.EventID, part.Index, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var storedDigest string
	var storedPayload []byte
	if err := tx.QueryRow(ctx, `
		SELECT digest, payload FROM audit.event_assembly_parts WHERE event_id = $1 AND part_index = $2
	`, part.EventID, int64(part.Index)).Scan(&storedDigest, &storedPayload); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.part_read_failed", slog.String("event_id", part.EventID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("read audit event %s part %d: %w", part.EventID, part.Index, err)
	}
	if storedDigest != part.Digest || !bytes.Equal(storedPayload, part.Payload) {
		return rejectTransport(ctx, part.EventID, fmt.Sprintf("has part %d that conflicts with the stored part", part.Index))
	}
	return nil
}

// storeAssemblyHeader stores the manifest bytes and commit version only
// when the assembly does not have a header.
func storeAssemblyHeader(ctx context.Context, tx pgx.Tx, message transportMessage, raw []byte) error {
	if _, err := tx.Exec(ctx, `
		UPDATE audit.event_assemblies
		   SET header = $2, commit_version = $3
		 WHERE event_id = $1 AND header IS NULL
	`, message.EventID, raw, message.CommitVersion); err != nil {
		slog.ErrorContext(ctx, "audit.consumer.header_store_failed", slog.String("event_id", message.EventID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("store audit event %s manifest: %w", message.EventID, err)
	}
	return nil
}

// rejectTransport logs the rejection and wraps errMalformedPayload.
// The consumer writes records with this error to the dead-letter table.
func rejectTransport(ctx context.Context, eventID uuid.UUID, reason string) error {
	slog.ErrorContext(ctx, "audit.consumer.transport_rejected", slog.String("event_id", eventID.String()), slog.String("err", reason))
	return fmt.Errorf("%w: audit event %s %s", errMalformedPayload, eventID, reason)
}

// partFromMessage returns the EventPart fields from a part message.
func partFromMessage(message transportMessage) EventPart {
	return EventPart{
		EventID: message.EventID, Index: message.PartIndex, Count: message.PartCount,
		Digest: message.PartDigest, TransportDigest: message.TransportDigest, Payload: message.Payload,
	}
}
