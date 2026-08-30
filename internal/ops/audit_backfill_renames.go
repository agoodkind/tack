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

// referenceRenameEvent builds one reconstructed rename. The resolution is nil
// when the renamed node no longer exists: the rename still happened, so the
// event is still owed, and what the deletion cost is the node's type, which the
// event marks rather than guesses.
func referenceRenameEvent(
	ctx context.Context,
	resolutions renameResolutions,
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
	resolution := resolutions.ByNode[nodeID]
	nodeType := ""
	if resolution != nil {
		nodeType = resolution.NodeType
	}
	extra, err := json.Marshal(referenceRenameExtra{
		OldReference: rename.OldReference, NewReference: rename.NewReference, Reconstruction: true,
		HistoricalTime: historicalReferenceRenameTime(rename), Evidence: referenceRenameEvidenceCitation,
		SubjectAbsent: resolution == nil,
	})
	if err != nil {
		wrapped := fmt.Errorf("encode reconstructed reference rename %s: %w", rename.NewReference, err)
		telemetry.L(ctx).Error("audit.reference_rename_backfill.extra_encode_failed", slog.String("err", wrapped.Error()))
		return audit.Event{}, wrapped
	}
	return audit.Event{
		Verb: string(audit.VerbNodeReferenceRename), EventID: referenceRenameEventID(rename),
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{Type: "node", NodeType: nodeType, ID: nodeID, Identifier: rename.NewReference, Name: ""},
		Context: audit.EventContext{
			OrgID: resolutions.OrgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
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
