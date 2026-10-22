package foundationdb

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// AppendChanges builds one format 2 change event for each change under
// operation, encodes each event with audit.EncodeEvent, and writes the
// encoded events to the outbox in tr with one OutboxWriter. A transaction
// attempt runs AppendChanges once, after it makes its changes, and writes no
// other outbox entry.
func AppendChanges(ctx context.Context, tr fdb.Transaction, operation audit.Event, changes []audit.Change) error {
	if len(changes) == 0 {
		return nil
	}
	outbox := NewOutboxWriter(tr)
	completedAt := clock.Now().UTC()
	for _, change := range changes {
		encoded, err := encodeChangeEvent(operation, change, completedAt)
		if err != nil {
			slog.ErrorContext(ctx, "audit.changes.append_failed", slog.String("event_id", change.EventID.String()), slog.String("err", err.Error()))
			return fmt.Errorf("append change event %s: %w", change.EventID, err)
		}
		if err := outbox.WriteEncodedEvent(ctx, encoded); err != nil {
			return err
		}
	}
	return nil
}

// encodeChangeEvent builds and encodes the change event for change.
func encodeChangeEvent(operation audit.Event, change audit.Change, completedAt time.Time) (audit.EncodedEvent, error) {
	event, err := audit.ChangeEvent(operation, change, completedAt)
	if err != nil {
		return audit.EncodedEvent{}, changeEncodeFailure("build", change, err)
	}
	encoded, err := audit.EncodeEvent(event, nil)
	if err != nil {
		return audit.EncodedEvent{}, changeEncodeFailure("encode", change, err)
	}
	return encoded, nil
}

// changeEncodeFailure logs and wraps err with the failed step and the event
// ID of change.
func changeEncodeFailure(step string, change audit.Change, err error) error {
	slog.Error("audit.changes.encode_failed", slog.String("step", step), slog.String("event_id", change.EventID.String()), slog.String("err", err.Error()))
	return fmt.Errorf("%s the change event %s: %w", step, change.EventID, err)
}

// mutationAudit records the changes of one write. It reads the operation from
// the request context and allocates the base event ID before the first
// transaction attempt. Each attempt derives the same event ID for the same
// change of the same record.
type mutationAudit struct {
	operation audit.Event
	base      uuid.UUID
	changes   []audit.Change
}

// beginMutationAudit returns the recorder for one write to orgID. It returns
// an error that wraps audit.ErrMissingMutationContext when ctx does not
// identify the actor and operation.
func beginMutationAudit(ctx context.Context, orgID uuid.UUID) (*mutationAudit, error) {
	operation, err := audit.RequireMutationContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("record changes in org %s: %w", orgID, err)
	}
	base, err := uuid.NewV7()
	if err != nil {
		slog.ErrorContext(ctx, "audit.changes.id_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("create the change event base ID: %w", err)
	}
	operation.Context.OrgID = orgID
	return &mutationAudit{operation: operation, base: base, changes: nil}, nil
}

// jobMutationAudit returns the recorder for one step of a persistent job.
// The base event ID derives from the operation ID and the job ID. Every
// attempt and every retry of the step derives the same event ID for the same
// change of the same record.
func jobMutationAudit(operation audit.Event, orgID, jobID uuid.UUID) *mutationAudit {
	operation.Context.OrgID = orgID
	base := uuid.NewSHA1(operation.Context.OperationID, []byte("job:"+jobID.String()))
	return &mutationAudit{operation: operation, base: base, changes: nil}
}

// startAttempt removes the changes of an earlier transaction attempt.
func (m *mutationAudit) startAttempt() {
	m.changes = m.changes[:0]
}

// record adds the change of one record with its complete old and new values.
// A nil or empty value identifies an absent record. Equal values add nothing.
func (m *mutationAudit) record(target audit.Target, before, after []byte) error {
	if len(before) == 0 && len(after) == 0 {
		return nil
	}
	if string(before) == string(after) {
		return nil
	}
	change, err := audit.NewChange(m.eventID(target, before, after), target, before, after)
	if err != nil {
		slog.Error("audit.changes.record_failed", slog.String("target", targetKey(target)), slog.String("err", err.Error()))
		return fmt.Errorf("record the change of %s: %w", targetKey(target), err)
	}
	m.changes = append(m.changes, change)
	return nil
}

// eventID derives the event ID from the base ID, the change kind, and the
// target identity.
func (m *mutationAudit) eventID(target audit.Target, before, after []byte) uuid.UUID {
	kind := "update"
	switch {
	case len(before) == 0:
		kind = "create"
	case len(after) == 0:
		kind = "delete"
	}
	return uuid.NewSHA1(m.base, []byte(kind+":"+targetKey(target)))
}

// append writes the recorded change events to the outbox in tr.
func (m *mutationAudit) append(ctx context.Context, tr fdb.Transaction) error {
	return AppendChanges(ctx, tr, m.operation, m.changes)
}

// finish marks the request slot committed when the committed attempt
// recorded at least one change.
func (m *mutationAudit) finish(ctx context.Context) {
	if len(m.changes) > 0 {
		commitStagedIntent(ctx)
	}
}

func targetKey(target audit.Target) string {
	if target.Kind == audit.TargetKindRelationship {
		return target.Kind + ":" + target.SourceID.String() + ":" + target.RelationType + ":" + target.TargetID.String()
	}
	return target.Kind + ":" + target.NodeType + ":" + target.ID.String()
}
