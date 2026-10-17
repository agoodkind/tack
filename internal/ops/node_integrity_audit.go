package ops

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// repairEventNamespace is the UUID namespace of repair event IDs. Each phase
// of each repair has one event ID, and every attempt of that phase reuses it.
var repairEventNamespace = uuid.MustParse("4941c64c-d9f0-4e71-a7ea-2eed08231ab5")

// repairEventTarget identifies the node that one repair phase changes.
type repairEventTarget struct {
	orgID    uuid.UUID
	nodeID   uuid.UUID
	nodeType string
	name     string
}

// stageRepairEvent creates an audit slot and stages the ledger event for
// one repair phase in the returned context. The event uses the operator
// principal as its actor. The checked mutation finalizes the delta from
// the records that its transaction reads and writes.
func stageRepairEvent(ctx context.Context, run repairPhaseRun, phase node.RepairPhase, verb audit.Verb, target repairEventTarget) (context.Context, error) {
	identity := run.entry.RepairID + ":" + string(phase)
	event := audit.Event{
		Verb: string(verb), EventID: uuid.NewSHA1(repairEventNamespace, []byte(identity)),
		Actor: audit.Actor{
			Type: run.operator.ActorType(), ID: run.operator.ID, Email: run.operator.Email, Name: run.operator.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{Type: "node", NodeType: target.nodeType, ID: target.nodeID, Identifier: "", Name: target.name},
		Context: audit.EventContext{
			OrgID: target.orgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: telemetry.RequestID(ctx), TraceID: telemetry.TraceID(ctx),
			Source: audit.SourceSystem, Tool: nodeIntegrityBackfillTool, RPC: "",
			Reason: nodeIntegrityTicket + " repair " + identity,
		},
		Delta: nil, Outcome: audit.OutcomeOK, Error: nil, IdempotencyKey: "repair:" + identity,
		OccurredAt: clock.Now().UTC(), Extra: nil,
	}
	payload, err := audit.MarshalEvent(event)
	if err != nil {
		wrapped := fmt.Errorf("encode the ledger event of repair %s: %w", identity, err)
		slog.ErrorContext(ctx, "repair.event_failed", slog.String("err", wrapped.Error()), slog.String("repair_id", run.entry.RepairID))
		return ctx, wrapped
	}
	staged := auditintent.WithSlot(ctx, nodeIntegrityBackfillTool, run.operator.ID)
	if !auditintent.StageMutation(staged, payload, audit.FinalizeMutationEvent) {
		wrapped := fmt.Errorf("stage the ledger event of repair %s", identity)
		slog.ErrorContext(ctx, "repair.event_failed", slog.String("err", wrapped.Error()), slog.String("repair_id", run.entry.RepairID))
		return ctx, wrapped
	}
	return staged, nil
}
