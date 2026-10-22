package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// ProtectedPayload contains a stored JSON value and its redaction flag.
// Value is nil after redaction.
type ProtectedPayload struct {
	Value    json.RawMessage
	Redacted bool
}

// PIIRefsForSubject lists audit.pii references for subjectID within orgID,
// including references to redacted rows.
func (r *Redactor) PIIRefsForSubject(ctx context.Context, orgID, subjectID uuid.UUID) ([]uuid.UUID, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("audit redactor not configured")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT pii_ref FROM audit.pii
		 WHERE org_id = $1 AND subject_id = $2
	`, orgID, subjectID)
	if err != nil {
		slog.ErrorContext(ctx, "audit.pii.subject_refs_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read audit.pii references for subject %s in org %s: %w", subjectID, orgID, err)
	}
	defer rows.Close()
	refs := make([]uuid.UUID, 0)
	for rows.Next() {
		var ref uuid.UUID
		if err := rows.Scan(&ref); err != nil {
			slog.ErrorContext(ctx, "audit.pii.subject_ref_scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("scan audit.pii reference for subject %s: %w", subjectID, err)
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.pii.subject_refs_rows_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read audit.pii reference rows for subject %s: %w", subjectID, err)
	}
	return refs, nil
}

// ProtectedValues reads payloads and redaction flags for refs from audit.pii.
// It uses the audit_redactor connection because audit_reader cannot select
// from audit.pii.
func (r *Redactor) ProtectedValues(ctx context.Context, refs []uuid.UUID) (map[uuid.UUID]ProtectedPayload, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("audit redactor not configured")
	}
	values := map[uuid.UUID]ProtectedPayload{}
	if len(refs) == 0 {
		return values, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT pii_ref, payload, redacted FROM audit.pii
		 WHERE pii_ref = ANY($1::uuid[])
	`, uuidStrings(refs))
	if err != nil {
		slog.ErrorContext(ctx, "audit.pii.values_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read audit protected values: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ref uuid.UUID
		var payload ProtectedPayload
		if err := rows.Scan(&ref, &payload.Value, &payload.Redacted); err != nil {
			slog.ErrorContext(ctx, "audit.pii.value_scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("scan audit protected value: %w", err)
		}
		values[ref] = payload
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.pii.values_rows_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read audit protected value rows: %w", err)
	}
	return values, nil
}
