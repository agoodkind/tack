package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// FixedNodeTypeStore scans stored node types in bounded pages. It replaces
// a node type only when the stored value still matches the scanned record.
type FixedNodeTypeStore interface {
	ScanTypes(ctx context.Context, cursor string) ([]*node.NodeType, string, error)
	Replace(ctx context.Context, before, after *node.NodeType) (bool, error)
}

// FixedNodeTypeAudit stages an audit event for the correction from before
// to after. It returns the context with the staged event.
type FixedNodeTypeAudit func(ctx context.Context, before, after *node.NodeType) (context.Context, error)

// FixedNodeTypeResult counts scanned, corrected, unchanged, and conflicted
// node types. During a dry run, it counts node types that need correction as
// corrected. A conflicted node type changed after the scan read it, or it is
// a guarded type that stores a CanLiveUnder value other than the earlier and
// current seeded values. Changes lists each corrected or conflicted node
// type's org ID, node type ID, type key, outcome, and CanLiveUnder values
// before and after correction. Dry runs and execute runs both populate
// Changes.
type FixedNodeTypeResult struct {
	Scanned       int                   `json:"scanned"`
	Corrected     int                   `json:"corrected"`
	Unchanged     int                   `json:"unchanged"`
	Conflicted    int                   `json:"conflicted"`
	CorrectedIDs  []uuid.UUID           `json:"corrected_ids,omitempty"`
	ConflictedIDs []uuid.UUID           `json:"conflicted_ids,omitempty"`
	Changes       []FixedNodeTypeChange `json:"changes,omitempty"`
}

// CorrectFixedNodeTypes calls ScanTypes to read every stored node type in
// every organization in bounded pages. It corrects node types with a seeded
// built-in ID through planNodeTypeCorrection, which replaces only
// CanLiveUnder. A dry run does not write. For each
// correction, CorrectFixedNodeTypes calls stage and uses store.Replace to
// commit the node type and audit event only when the stored node type still
// matches the scanned record.
func CorrectFixedNodeTypes(ctx context.Context, store FixedNodeTypeStore, stage FixedNodeTypeAudit, dryRun bool) (FixedNodeTypeResult, error) {
	result := FixedNodeTypeResult{Scanned: 0, Corrected: 0, Unchanged: 0, Conflicted: 0, CorrectedIDs: nil, ConflictedIDs: nil, Changes: nil}
	seeded := seededNodeTypes{orgID: uuid.Nil, byID: nil}
	cursor := ""
	for {
		types, next, err := store.ScanTypes(ctx, cursor)
		if err != nil {
			return result, fixedNodeTypeFailure(ctx, "scan node types after cursor "+cursor, err)
		}
		for _, stored := range types {
			if err := correctNodeType(ctx, store, stage, stored, seeded.lookup(stored.OrgID, stored.ID), dryRun, &result); err != nil {
				return result, err
			}
		}
		if next == "" {
			return result, nil
		}
		if next == cursor {
			return result, fixedNodeTypeFailure(ctx, "scan node types", errors.New("the scan cursor did not advance"))
		}
		cursor = next
	}
}

// correctNodeType counts the outcome for one stored node type. It applies
// the correction unless dryRun is true.
func correctNodeType(
	ctx context.Context, store FixedNodeTypeStore, stage FixedNodeTypeAudit, stored, seed *node.NodeType, dryRun bool, result *FixedNodeTypeResult,
) error {
	result.Scanned++
	plan := planNodeTypeCorrection(stored, seed)
	if plan.Conflict != "" {
		result.Conflicted++
		result.ConflictedIDs = append(result.ConflictedIDs, stored.ID)
		change := nodeTypeChange(FixedOutcomeConflicted, stored, plan.Proposed)
		change.Reason = plan.Conflict
		result.Changes = append(result.Changes, change)
		telemetry.L(ctx).InfoContext(ctx, "metadata.fixed_node_type.conflicted", slog.String("org_id", stored.OrgID.String()),
			slog.String("node_type_id", stored.ID.String()), slog.String("type_key", stored.TypeKey), slog.String("reason", plan.Conflict))
		return nil
	}
	corrected := plan.Corrected
	if corrected == nil {
		result.Unchanged++
		return nil
	}
	if dryRun {
		result.Corrected++
		result.CorrectedIDs = append(result.CorrectedIDs, stored.ID)
		result.Changes = append(result.Changes, nodeTypeChange(FixedOutcomeCorrected, stored, corrected))
		return nil
	}
	staged, err := stage(ctx, stored, corrected)
	if err != nil {
		return fixedNodeTypeFailure(ctx, "stage the correction audit of node type "+stored.ID.String(), err)
	}
	replaced, err := store.Replace(staged, stored, corrected)
	if err != nil {
		return fixedNodeTypeFailure(ctx, "correct node type "+stored.ID.String(), err)
	}
	if !replaced {
		result.Conflicted++
		result.ConflictedIDs = append(result.ConflictedIDs, stored.ID)
		result.Changes = append(result.Changes, nodeTypeChange(FixedOutcomeConflicted, stored, corrected))
		return nil
	}
	result.Corrected++
	result.CorrectedIDs = append(result.CorrectedIDs, stored.ID)
	result.Changes = append(result.Changes, nodeTypeChange(FixedOutcomeCorrected, stored, corrected))
	telemetry.L(ctx).InfoContext(ctx, "metadata.fixed_node_type.corrected", slog.String("org_id", stored.OrgID.String()),
		slog.String("node_type_id", stored.ID.String()), slog.String("type_key", stored.TypeKey))
	return nil
}

// seededNodeTypes caches seeded node types by ID for one organization.
// When lookup requests a different organization, seededNodeTypes replaces
// the cached node types.
type seededNodeTypes struct {
	orgID uuid.UUID
	byID  map[uuid.UUID]*node.NodeType
}

func (s *seededNodeTypes) lookup(orgID, typeID uuid.UUID) *node.NodeType {
	if s.byID == nil || s.orgID != orgID {
		types := defaultNodeTypes(orgID)
		s.orgID, s.byID = orgID, make(map[uuid.UUID]*node.NodeType, len(types))
		for _, nodeType := range types {
			s.byID[nodeType.ID] = nodeType
		}
	}
	return s.byID[typeID]
}

func fixedNodeTypeFailure(ctx context.Context, operation string, err error) error {
	slog.ErrorContext(ctx, "metadata.fixed_node_type.correction_failed", slog.String("err", err.Error()))
	return fmt.Errorf("correct fixed node types: %s: %w", operation, err)
}
