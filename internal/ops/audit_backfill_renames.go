package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

func referenceRenameEvent(
	ctx context.Context,
	reader referenceRenameResolver,
	principal audit.OperatorPrincipal,
	rename referenceRenameEvidence,
	occurredAt time.Time,
) (audit.Event, error) {
	nodeID, err := uuid.Parse(rename.NodeID)
	if err != nil {
		wrapped := fmt.Errorf("parse evidence node %q: %w", rename.NodeID, err)
		telemetry.L(ctx).Error("audit.reference_rename_backfill.node_parse_failed", slog.String("err", wrapped.Error()))
		return audit.Event{}, wrapped
	}
	resolution, err := reader.Resolve(ctx, nodeID)
	if err != nil {
		wrapped := fmt.Errorf("resolve evidence node %s: %w", nodeID, err)
		telemetry.L(ctx).Error("audit.reference_rename_backfill.node_resolve_failed", slog.String("err", wrapped.Error()))
		return audit.Event{}, wrapped
	}
	extra, err := json.Marshal(referenceRenameExtra{
		OldReference: rename.OldReference, NewReference: rename.NewReference, Reconstruction: true,
		HistoricalTime: historicalReferenceRenameTime(rename), Evidence: referenceRenameEvidenceCitation,
	})
	if err != nil {
		wrapped := fmt.Errorf("encode reconstructed reference rename %s: %w", rename.NewReference, err)
		telemetry.L(ctx).Error("audit.reference_rename_backfill.extra_encode_failed", slog.String("err", wrapped.Error()))
		return audit.Event{}, wrapped
	}
	return audit.Event{
		Verb: string(audit.VerbNodeReferenceRename), EventID: referenceRenameEventID(rename),
		Actor: audit.Actor{
			Type: audit.ActorOperator, ID: principal.ID, Email: principal.Email, Name: principal.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{Type: "node", NodeType: resolution.NodeType, ID: nodeID, Identifier: rename.NewReference, Name: ""},
		Context: audit.EventContext{
			OrgID: resolution.OrgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: audit.SourceSystem, Tool: "", RPC: "", Reason: "",
		},
		Delta: nil, Outcome: audit.OutcomeOK, Error: nil, IdempotencyKey: referenceRenameIdempotencyKey(rename), OccurredAt: occurredAt.UTC(), Extra: extra,
	}, nil
}

func historicalReferenceRenameTime(rename referenceRenameEvidence) string {
	if rename.OldReference == followupOldReference && rename.NewReference == followupNewReference {
		return referenceRepairFollowupDate
	}
	return referenceRepairDate
}

func referenceRenameEventID(rename referenceRenameEvidence) uuid.UUID {
	return uuid.NewSHA1(referenceRenameBackfillNamespace, []byte(referenceRenameIdempotencyKey(rename)))
}

func referenceRenameIdempotencyKey(rename referenceRenameEvidence) string {
	return referenceRenameBackfillPrefix + rename.NodeID + ":" + rename.OldReference + ":" +
		rename.NewReference + ":" + historicalReferenceRenameTime(rename)
}
