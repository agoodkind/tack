package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ProtectedValue pairs a delta reference with its subject ID and JSON value
// for storage in audit.pii.
type ProtectedValue struct {
	Ref       uuid.UUID       `json:"ref"`
	SubjectID uuid.UUID       `json:"subject_id"`
	Value     json.RawMessage `json:"value"`
}

// ProtectedReference replaces a raw delta value with a one-key JSON object
// containing the value's pii_ref UUID.
type ProtectedReference struct {
	PIIRef uuid.UUID `json:"pii_ref"`
}

// protectedReferenceKey is the JSON key for a protected value reference.
const protectedReferenceKey = "pii_ref"

// validateProtected requires exactly one Protected value per delta reference,
// valid JSON for each value, and SubjectID equal to Context.Target.ID.
func validateProtected(event Event) error {
	references := map[uuid.UUID]bool{}
	if event.Delta != nil {
		var err error
		references, err = deltaReferences(event.Delta)
		if err != nil {
			return err
		}
	}
	if len(references) == 0 && len(event.Protected) == 0 {
		return nil
	}
	if event.Context.Target == nil {
		return errors.New("protected values require a target")
	}
	if len(references) != len(event.Protected) {
		return fmt.Errorf("delta has %d protected references for %d protected values", len(references), len(event.Protected))
	}
	for _, value := range event.Protected {
		switch {
		case value.SubjectID != event.Context.Target.ID:
			return fmt.Errorf("protected value %s subject %s does not match target %s", value.Ref, value.SubjectID, event.Context.Target.ID)
		case !references[value.Ref]:
			return fmt.Errorf("protected value %s does not have a reference in the delta", value.Ref)
		case !json.Valid(value.Value):
			return fmt.Errorf("protected value %s is not valid JSON", value.Ref)
		}
		delete(references, value.Ref)
	}
	return nil
}

// deltaReferences collects protected references from top-level members of
// Before and After. It rejects nil and repeated references.
func deltaReferences(delta *Delta) (map[uuid.UUID]bool, error) {
	references := map[uuid.UUID]bool{}
	for _, record := range []json.RawMessage{delta.Before, delta.After} {
		if len(record) == 0 {
			continue
		}
		var members map[string]json.RawMessage
		if err := json.Unmarshal(record, &members); err != nil {
			continue
		}
		for _, member := range members {
			ref, found := protectedReference(member)
			if !found {
				continue
			}
			if ref == uuid.Nil {
				return nil, errors.New("delta has an invalid protected reference")
			}
			if references[ref] {
				return nil, fmt.Errorf("delta repeats protected reference %s", ref)
			}
			references[ref] = true
		}
	}
	return references, nil
}

// protectedReference returns the UUID and reports whether member is a
// one-key pii_ref object. It returns [uuid.Nil] and true when that object
// contains an invalid UUID.
func protectedReference(member json.RawMessage) (uuid.UUID, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(member, &fields); err != nil || len(fields) != 1 {
		return uuid.Nil, false
	}
	if _, found := fields[protectedReferenceKey]; !found {
		return uuid.Nil, false
	}
	var reference ProtectedReference
	if err := json.Unmarshal(member, &reference); err != nil {
		return uuid.Nil, true
	}
	return reference.PIIRef, true
}

// writeEventPII writes the actor's personal data, then the event's protected
// values in tx. It returns the actor reference or [uuid.Nil] when the actor
// does not have personal data.
func writeEventPII(ctx context.Context, tx pgx.Tx, event Event) (uuid.UUID, error) {
	piiRef, err := writePIIRow(ctx, tx, event.Actor)
	if err != nil {
		return uuid.Nil, err
	}
	if err := writeProtectedValues(ctx, tx, event); err != nil {
		return uuid.Nil, err
	}
	return piiRef, nil
}

// writeProtectedValues validates the event's protected references and values,
// even when Protected is empty. It marks validation errors as malformed
// payloads and inserts valid values into audit.pii with subject and org IDs.
// It skips inserts that conflict with existing rows.
func writeProtectedValues(ctx context.Context, tx pgx.Tx, event Event) error {
	if err := validateProtected(event); err != nil {
		slog.ErrorContext(ctx, "audit.protected.invalid",
			slog.String("event_id", event.EventID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("%w: validate protected values for event %s: %w", errMalformedPayload, event.EventID, err)
	}
	for _, value := range event.Protected {
		if _, err := tx.Exec(ctx, `
			INSERT INTO audit.pii (pii_ref, payload, redacted, subject_id, org_id)
			VALUES ($1, $2, false, $3, $4)
			ON CONFLICT DO NOTHING
		`, value.Ref, []byte(value.Value), value.SubjectID, event.Context.OrgID); err != nil {
			slog.ErrorContext(ctx, "audit.protected.insert_failed",
				slog.String("event_id", event.EventID.String()), slog.String("err", err.Error()))
			return fmt.Errorf("insert protected value %s of event %s: %w", value.Ref, event.EventID, err)
		}
	}
	return nil
}
