package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

func deriveReferenceKeyClasses(
	ctx context.Context,
	env *Env,
	principal audit.OperatorPrincipal,
	orgID uuid.UUID,
	occurredAt time.Time,
	repairStart time.Time,
) ([]auditBackfillClass, error) {
	renames, err := referenceRenameEvidenceByNode(ctx)
	if err != nil {
		return nil, err
	}
	followupNodeID, err := followupReferenceKeyNodeID(ctx, renames)
	if err != nil {
		return nil, err
	}
	keys, err := enumerateReferenceKeys(ctx, env, orgID, &repairStart)
	if err != nil {
		return nil, err
	}
	sortedReferenceKeys(keys)
	initial := auditBackfillClass{Count: auditBackfillCount{Class: "reference keys 2026-08-07", Derived: 0, Recorded: recordedReferenceKeys}, Events: nil}
	followup := auditBackfillClass{Count: auditBackfillCount{Class: "reference keys 2026-08-08", Derived: 0, Recorded: recordedFollowupReferenceKey}, Events: nil}
	for _, key := range keys {
		date := referenceRepairDate
		class := &initial
		if key.View.ID == followupNodeID {
			date = referenceRepairFollowupDate
			class = &followup
		}
		event, eventErr := referenceKeyEvent(ctx, principal, orgID, occurredAt, key, renames[key.View.ID], date)
		if eventErr != nil {
			return nil, eventErr
		}
		class.Events = append(class.Events, event)
	}
	initial.Count.Derived = len(initial.Events)
	followup.Count.Derived = len(followup.Events)
	return []auditBackfillClass{initial, followup}, nil
}

func referenceKeyEvent(
	ctx context.Context,
	principal audit.OperatorPrincipal,
	orgID uuid.UUID,
	occurredAt time.Time,
	key repairedReferenceKey,
	rename referenceRenameEvidence,
	date string,
) (audit.Event, error) {
	entity := audit.Entity{Type: "node", NodeType: key.View.NodeType, ID: key.View.ID, Identifier: "", Name: ""}
	extra := reconstructionExtra{
		Class: "reference keys", Reconstruction: true, HistoricalTime: date,
		Evidence: referenceRepairEvidence, Scope: "", Template: "", Run: "",
		SeedEmail: "", OrgSlug: "", WorkspaceSlug: "", HistoricalReferenceKey: "",
		HistoricalReferenceKeyTextUnproven: false, ObservedReferenceKey: "",
		ObservedReferenceTemplate: "", HistoricalDefinitionSetUnproven: false,
		ObservedSeedDefinition: nil,
	}
	if rename.NodeID != "" {
		entity.Identifier = rename.NewReference
		extra.HistoricalReferenceKey = rename.NewReference
	} else {
		// The creation bound proves the repair wrote a key for this node, not
		// which key it wrote. Keep the live value explicitly observed so it
		// cannot be read as the historical value after a later rename or move.
		extra.HistoricalReferenceKeyTextUnproven = true
		extra.ObservedReferenceKey = key.Key.Encoded
		extra.ObservedReferenceTemplate = key.Key.TemplateName
	}
	return reconstructionEvent(
		ctx,
		audit.VerbOpsRepairReferenceUniqueness,
		principal,
		entity,
		orgID,
		"tack-429-reference-key:"+key.View.ID.String()+":"+key.Key.TemplateName+":"+date,
		extra,
		occurredAt,
	)
}

// referenceRenameEvidenceByNode indexes the rename evidence by node, keeping
// the last rename in each node's chain, which is the reference that node ended
// the repair holding.
//
// One node was renamed on both repair days, so it owns two records. Which one
// is later is decided by the chain itself: the later record's old reference is
// the earlier record's new reference. Reading it from the order of the file
// instead would silently pick the wrong reference the first time the file is
// sorted or extended, and the event would then assert a historical key the
// repair never wrote. A pair that does not form a chain is not evidence of
// anything, so it fails the run.
func referenceRenameEvidenceByNode(ctx context.Context) (map[uuid.UUID]referenceRenameEvidence, error) {
	renames, err := loadReferenceRenameEvidence(ctx)
	if err != nil {
		return nil, err
	}
	return indexReferenceRenameEvidence(ctx, renames)
}

// indexReferenceRenameEvidence performs the indexing on a supplied slice so a
// test can vary the order the records arrive in, which is the property that
// matters and the one the embedded file cannot vary.
func indexReferenceRenameEvidence(
	ctx context.Context,
	renames []referenceRenameEvidence,
) (map[uuid.UUID]referenceRenameEvidence, error) {
	byNode := make(map[uuid.UUID]referenceRenameEvidence, len(renames))
	for _, rename := range renames {
		nodeID, parseErr := uuid.Parse(rename.NodeID)
		if parseErr != nil {
			wrapped := fmt.Errorf("parse reference rename evidence node %q: %w", rename.NodeID, parseErr)
			telemetry.L(ctx).Error("audit.reference_repair_backfill.evidence_node_parse_failed", slog.String("err", wrapped.Error()))
			return nil, wrapped
		}
		existing, seen := byNode[nodeID]
		if !seen {
			byNode[nodeID] = rename
			continue
		}
		later, chainErr := laterRenameInChain(ctx, existing, rename)
		if chainErr != nil {
			return nil, chainErr
		}
		byNode[nodeID] = later
	}
	return byNode, nil
}

// laterRenameInChain returns whichever of two renames of the same node came
// second, proven by one taking the other's new reference as its old one.
func laterRenameInChain(ctx context.Context, first, second referenceRenameEvidence) (referenceRenameEvidence, error) {
	if second.OldReference == first.NewReference {
		return second, nil
	}
	if first.OldReference == second.NewReference {
		return first, nil
	}
	err := fmt.Errorf(
		"reference rename evidence for node %s does not form a chain: %s to %s and %s to %s",
		first.NodeID, first.OldReference, first.NewReference, second.OldReference, second.NewReference,
	)
	telemetry.L(ctx).Error("audit.reference_repair_backfill.evidence_chain_broken", slog.String("err", err.Error()))
	return referenceRenameEvidence{OldReference: "", NewReference: "", NodeID: ""}, err
}

func followupReferenceKeyNodeID(ctx context.Context, renames map[uuid.UUID]referenceRenameEvidence) (uuid.UUID, error) {
	for nodeID, rename := range renames {
		if rename.OldReference == followupOldReference && rename.NewReference == followupNewReference {
			return nodeID, nil
		}
	}
	err := fmt.Errorf("reference rename evidence has no %s to %s follow-up", followupOldReference, followupNewReference)
	telemetry.L(ctx).Error("audit.reference_repair_backfill.followup_node_missing", slog.String("err", err.Error()))
	return uuid.Nil, err
}
