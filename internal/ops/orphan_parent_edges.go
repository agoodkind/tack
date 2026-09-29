package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// ParentEdgeRemover removes one relationship in one transaction. The
// transaction writes the ledger event staged on ctx.
type ParentEdgeRemover interface {
	Remove(ctx context.Context, orgID, sourceID uuid.UUID, relationType string, targetID uuid.UUID) error
}

// removeExtraParentEdges removes each child_of edge in edges and returns the
// count of removed edges. Each removal writes one relationship.remove ledger
// event with the operator as its actor, in the removal transaction.
func removeExtraParentEdges(ctx context.Context, remover ParentEdgeRemover, principal audit.OperatorPrincipal, edges []fdbadapter.ParentEdge) (int, error) {
	removed := 0
	for _, edge := range edges {
		staged, err := stageEdgeRemovalEvent(ctx, principal, edge)
		if err != nil {
			return removed, err
		}
		if err := remover.Remove(staged, edge.OrgID, edge.NodeID, node.RelChildOf, edge.TargetID); err != nil {
			wrapped := fmt.Errorf("remove child_of edge from %s to %s: %w", edge.NodeID, edge.TargetID, err)
			slog.ErrorContext(ctx, "orphan_nodes.edge_remove_failed", slog.String("err", wrapped.Error()))
			return removed, wrapped
		}
		removed++
	}
	return removed, nil
}

// stageEdgeRemovalEvent returns ctx with the relationship.remove ledger event
// of one edge staged on a new audit slot.
func stageEdgeRemovalEvent(ctx context.Context, principal audit.OperatorPrincipal, edge fdbadapter.ParentEdge) (context.Context, error) {
	eventID, err := uuid.NewV7()
	if err != nil {
		wrapped := fmt.Errorf("create the removal event id of edge %s to %s: %w", edge.NodeID, edge.TargetID, err)
		slog.ErrorContext(ctx, "orphan_nodes.event_failed", slog.String("err", wrapped.Error()))
		return ctx, wrapped
	}
	event := audit.Event{
		Verb: string(audit.VerbRelationshipRemove), EventID: eventID,
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{Type: "relationship", NodeType: "", ID: edge.NodeID, Identifier: node.RelChildOf, Name: edge.TargetID.String()},
		Context: audit.EventContext{
			OrgID: edge.OrgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: telemetry.RequestID(ctx), TraceID: telemetry.TraceID(ctx),
			Source: audit.SourceSystem, Tool: orphanBackfillTool, RPC: "", Reason: "",
		},
		Delta: nil, Outcome: audit.OutcomeOK, Error: nil, IdempotencyKey: "",
		OccurredAt: clock.Now().UTC(), Extra: nil,
	}
	payload, err := audit.MarshalEvent(event)
	if err != nil {
		wrapped := fmt.Errorf("encode the removal event of edge %s to %s: %w", edge.NodeID, edge.TargetID, err)
		slog.ErrorContext(ctx, "orphan_nodes.event_failed", slog.String("err", wrapped.Error()))
		return ctx, wrapped
	}
	staged := auditintent.WithSlot(ctx, orphanBackfillTool, principal.ID)
	auditintent.Stage(staged, payload)
	return staged, nil
}
