package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
)

// fixedNodeTypeFields stores one node type's parent type list for an audit
// delta.
type fixedNodeTypeFields struct {
	CanLiveUnder []string `json:"can_live_under"`
}

// RunFixedNodeTypeCorrection uses store to correct the CanLiveUnder lists
// of stored built-in node types in every organization. When dryRun is false,
// it stages a node_type.update audit event for each correction with the
// operator principal from ctx. A dry run does not write.
func RunFixedNodeTypeCorrection(ctx context.Context, store service.FixedNodeTypeStore, dryRun bool) (service.FixedNodeTypeResult, error) {
	result, err := service.CorrectFixedNodeTypes(ctx, store, stageFixedNodeTypeAudit, dryRun)
	if err != nil {
		wrapped := fmt.Errorf("run fixed node type correction: %w", err)
		slog.ErrorContext(ctx, "metadata.fixed_node_type.run_failed", slog.String("err", wrapped.Error()))
		return result, wrapped
	}
	return result, nil
}

// stageFixedNodeTypeAudit stages a node_type.update event on the context it
// returns and sets the source to migration. The delta records can_live_under
// before and after the correction. The event ID uses a name-based UUID of
// the organization, the node type, and the corrected list. Retrying the same
// correction uses the same event ID.
func stageFixedNodeTypeAudit(ctx context.Context, before, after *node.NodeType) (context.Context, error) {
	principal, ok := audit.OperatorPrincipalFromContext(ctx)
	if !ok {
		return ctx, fixedNodeTypeAuditFailure(ctx, after, errors.New("operator principal is missing"))
	}
	beforeJSON, err := json.Marshal(fixedNodeTypeFields{CanLiveUnder: before.CanLiveUnder})
	if err != nil {
		return ctx, fixedNodeTypeAuditFailure(ctx, after, err)
	}
	afterJSON, err := json.Marshal(fixedNodeTypeFields{CanLiveUnder: after.CanLiveUnder})
	if err != nil {
		return ctx, fixedNodeTypeAuditFailure(ctx, after, err)
	}
	event := audit.Event{
		Verb:    string(audit.VerbNodeTypeUpdate),
		EventID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("fixed-node-type:"+after.OrgID.String()+":"+after.ID.String()+":"+string(afterJSON))),
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{Type: "node_type", NodeType: after.TypeKey, ID: after.ID, Identifier: after.TypeKey, Name: after.Name},
		Context: audit.EventContext{
			OrgID: after.OrgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: telemetry.RequestID(ctx), TraceID: telemetry.TraceID(ctx),
			Source: audit.SourceMigration, Tool: fixedDefinitionsCommand, RPC: "", Reason: "",
		},
		Delta:   &audit.Delta{Before: beforeJSON, After: afterJSON, Changed: []string{"can_live_under"}},
		Outcome: audit.OutcomeOK, Error: nil, IdempotencyKey: "", OccurredAt: clock.Now().UTC(), Extra: nil,
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return ctx, fixedNodeTypeAuditFailure(ctx, after, err)
	}
	staged := auditintent.WithSlot(ctx, fixedDefinitionsCommand, principal.ID)
	if !auditintent.Stage(staged, payload) {
		return ctx, fixedNodeTypeAuditFailure(ctx, after, errors.New("the audit slot refused the event"))
	}
	return staged, nil
}

func fixedNodeTypeAuditFailure(ctx context.Context, nodeType *node.NodeType, err error) error {
	slog.ErrorContext(ctx, "metadata.fixed_node_type.audit_failed", slog.String("err", err.Error()),
		slog.String("node_type_id", nodeType.ID.String()))
	return fmt.Errorf("stage fixed node type audit for %s: %w", nodeType.ID, err)
}
