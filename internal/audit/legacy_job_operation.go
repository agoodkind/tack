package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

const legacyJobOperationName = "subtree-delete-operation"

// LegacyJobOperation builds the audit operation for a subtree delete job record
// that does not contain an operation. When template is not empty, it decodes
// template as an [Event] and copies its actor, source, tool, request ID, and trace
// ID. When template is empty, it uses a user actor with actorID and the system
// source, [SourceSystem].
//
// It derives a name-based UUID from jobID as the operation ID. Every step and
// every runner derive the same operation ID from the same job ID. StartedAt uses
// the time that the UUIDv7 jobID encodes. It returns an error that wraps
// [ErrMissingMutationContext] when jobID is not a UUIDv7, and an error when
// template is not valid event JSON.
//
// Remove AuditTemplate and [LegacyJobOperation] after job records with
// audit_template no longer exist in any environment.
func LegacyJobOperation(ctx context.Context, jobID, actorID uuid.UUID, template json.RawMessage) (Event, error) {
	if jobID.Version() != uuidVersionTimeOrdered {
		err := fmt.Errorf("%w: cannot derive the start time from the non-UUIDv7 ID of subtree delete job %s", ErrMissingMutationContext, jobID)
		slog.ErrorContext(ctx, "audit.legacy_job_operation.id_failed", slog.String("err", err.Error()))
		return Event{}, err
	}
	actor := Actor{
		Type: ActorUser, ID: actorID, Email: "", Name: "", SessionID: "",
		IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
	}
	eventContext := EventContext{
		OrgID: uuid.Nil, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
		RequestID: "", TraceID: "", Source: SourceSystem, Tool: "", RPC: "", Reason: "",
	}
	if len(template) > 0 {
		var staged Event
		if err := json.Unmarshal(template, &staged); err != nil {
			slog.ErrorContext(ctx, "audit.legacy_job_operation.decode_failed", slog.String("err", err.Error()))
			return Event{}, fmt.Errorf("decode the audit template of subtree delete job %s: %w", jobID, err)
		}
		actor = staged.Actor
		eventContext.Source, eventContext.Tool = staged.Context.Source, staged.Context.Tool
		eventContext.RequestID, eventContext.TraceID = staged.Context.RequestID, staged.Context.TraceID
	}
	operation := operationEvent(actor, jobID, eventContext, nil)
	operation.Context.OperationID = uuid.NewSHA1(jobID, []byte(legacyJobOperationName))
	return operation, nil
}
