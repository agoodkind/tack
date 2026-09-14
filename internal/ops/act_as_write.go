// act_as_write.go is the recording half of the act-as command: the grant row
// written as the operator before the write, and the write itself made as the
// user with the operator's provenance staged on its row (TACK-424).

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/service"
)

// recordActAsGrant writes the grant row as the operator, on the target org,
// before the write it authorizes.
func recordActAsGrant(ctx context.Context, outbox audit.OutboxWriter, principal audit.OperatorPrincipal, grant actAsGrantExtra) error {
	encoded, err := json.Marshal(grant)
	if err != nil {
		slog.ErrorContext(ctx, "act_as.grant_encode_failed", slog.String("err", err.Error()))
		return fmt.Errorf("encode the act-as grant: %w", err)
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		slog.ErrorContext(ctx, "act_as.grant_event_id_failed", slog.String("err", err.Error()))
		return fmt.Errorf("generate the act-as grant event id: %w", err)
	}
	event := audit.Event{
		Verb: string(audit.VerbOpsActAsGrant), EventID: eventID,
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{Type: "user", NodeType: "", ID: grant.TargetUserID, Identifier: grant.TargetEmail, Name: ""},
		Context: audit.EventContext{
			OrgID: grant.OrgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: grant.ParentID,
			RequestID: "", TraceID: "", Source: audit.SourceOperator, Tool: actAsTool, RPC: "", Reason: grant.Reason,
		},
		Delta: nil, Outcome: audit.OutcomeOK, Error: nil, IdempotencyKey: "",
		OccurredAt: clock.Now().UTC(), Extra: encoded,
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), actAsRecordTimeout)
	defer cancel()
	if err := outbox.WriteOutbox(recordCtx, event); err != nil {
		slog.ErrorContext(ctx, "act_as.grant_record_failed", slog.String("err", err.Error()))
		return fmt.Errorf("record the act-as grant, so the write did not happen: %w", err)
	}
	return nil
}

// actAsCreated is what createAsUser reports back.
type actAsCreated struct {
	nodeID       uuid.UUID
	rowCommitted bool
}

// createAsUser makes the write with the user as the actor and the operator's
// provenance staged on the row, the same way the MCP wrapper stages a user's
// own write: the store commits the row inside the create transaction.
func createAsUser(ctx context.Context, nodes actAsNodeCreator, target actAsTarget, input actAsCreateInput, provenance audit.ActProvenance) (actAsCreated, error) {
	ctx = auth.WithUser(ctx, target.user.ID)
	ctx = audit.WithScopeBuilder(ctx)
	audit.SetScopeFields(ctx, audit.Scope{OrgID: target.orgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: target.parentID})
	ctx = auditintent.WithSlot(ctx, actAsTool, target.user.ID)
	ctx = audit.WithActProvenance(ctx, provenance)
	created, err := nodes.Create(ctx, service.CreateInput{
		ParentID: target.parentID, ScopeID: uuid.Nil, NodeTypeKey: strings.TrimSpace(input.NodeType),
		Name: strings.TrimSpace(input.Name), Props: nil, Relationships: nil, ActorID: target.user.ID,
		IdempotencyKey: "", IdempotencyFingerprint: "", IdempotencySource: "",
	})
	if err != nil {
		slog.ErrorContext(ctx, "act_as.create_failed", slog.String("err", err.Error()))
		return actAsCreated{nodeID: uuid.Nil, rowCommitted: false}, fmt.Errorf("create %s as %s: %w", input.NodeType, target.user.Email, err)
	}
	return actAsCreated{nodeID: created.View.ID, rowCommitted: auditintent.Committed(ctx)}, nil
}
