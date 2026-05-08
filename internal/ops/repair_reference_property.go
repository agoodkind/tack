package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// RepairCleanupPolicy controls whether source fields are removed after repair.
type RepairCleanupPolicy string

// RepairConflictPolicy controls candidate selection when inputs disagree.
type RepairConflictPolicy string

const (
	// RepairCleanupNone leaves source fields unchanged after setting the target property.
	RepairCleanupNone RepairCleanupPolicy = "none"
	// RepairCleanupSourceFields removes declared source fields after setting the target property.
	RepairCleanupSourceFields RepairCleanupPolicy = "source_fields"
	// RepairConflictPreferSource chooses a resolved source-field candidate before existing target values.
	RepairConflictPreferSource RepairConflictPolicy = "prefer_source"
	// RepairConflictPreferExisting chooses the existing target value before source-field candidates.
	RepairConflictPreferExisting RepairConflictPolicy = "prefer_existing"
	// RepairConflictPreferHighestRank chooses the candidate with the largest numeric rank property.
	RepairConflictPreferHighestRank RepairConflictPolicy = "prefer_highest_rank"
	// RepairConflictFail blocks the repair when resolved candidates point at different nodes.
	RepairConflictFail RepairConflictPolicy = "fail"
)

// RepairReferenceProfile describes one generic UUID reference-property repair.
type RepairReferenceProfile struct {
	Name                string                  `json:"name,omitempty"`
	TargetProperty      string                  `json:"target_property"`
	TargetTypeKey       string                  `json:"target_type_key,omitempty"`
	TargetMatchProperty string                  `json:"target_match_property,omitempty"`
	SourceFields        []string                `json:"source_fields"`
	ScopeFields         []string                `json:"scope_fields,omitempty"`
	RemoveFields        []string                `json:"remove_fields,omitempty"`
	RenameFields        []RepairRenameField     `json:"rename_fields,omitempty"`
	AppendTextFields    []RepairAppendTextField `json:"append_text_fields,omitempty"`
	Normalization       RepairNormalization     `json:"normalization"`
	UseDefaultFallback  bool                    `json:"use_default_fallback,omitempty"`
	CleanupBehavior     RepairCleanupPolicy     `json:"cleanup_behavior,omitempty"`
	ConflictPolicy      RepairConflictPolicy    `json:"conflict_policy,omitempty"`
	RankProperty        string                  `json:"rank_property,omitempty"`
}

// RepairNormalization controls operator-supplied source normalization.
type RepairNormalization struct {
	Trim     bool              `json:"trim,omitempty"`
	CaseFold bool              `json:"case_fold,omitempty"`
	ValueMap map[string]string `json:"value_map,omitempty"`
}

// RepairPreview describes the current repair decision for one node.
type RepairPreview struct {
	Class                RepairClass                `json:"class"`
	ProfileName          string                     `json:"profile_name,omitempty"`
	NodeID               uuid.UUID                  `json:"node_id"`
	NodeType             string                     `json:"node_type"`
	CurrentUpdatedAt     time.Time                  `json:"current_updated_at"`
	TargetProperty       string                     `json:"target_property"`
	TargetType           string                     `json:"target_type"`
	ObservedSources      []RepairObservedSource     `json:"observed_sources,omitempty"`
	Candidates           []RepairReferenceCandidate `json:"candidates,omitempty"`
	ChosenCandidate      *RepairReferenceCandidate  `json:"chosen_candidate,omitempty"`
	PlannedProps         map[string]json.RawMessage `json:"planned_props,omitempty"`
	PlannedRelationships []RepairRelationshipChange `json:"planned_relationships,omitempty"`
	Summary              string                     `json:"summary"`
	ConfirmationToken    string                     `json:"confirmation_token,omitempty"`
	NeedsRepair          bool                       `json:"needs_repair"`
	CanApply             bool                       `json:"can_apply"`
}

// RepairObservedSource records one source field read from the node under repair.
type RepairObservedSource struct {
	Field      string          `json:"field"`
	Raw        json.RawMessage `json:"raw,omitempty"`
	Normalized string          `json:"normalized,omitempty"`
	Present    bool            `json:"present"`
}

// RepairReferenceCandidate records one attempted target-node match.
type RepairReferenceCandidate struct {
	SourceField string    `json:"source_field"`
	Reference   string    `json:"reference"`
	NodeID      uuid.UUID `json:"node_id,omitempty"`
	NodeName    string    `json:"node_name,omitempty"`
	Rank        int64     `json:"rank,omitempty"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
}

func (c *RepairConsole) planReferenceProperty(ctx context.Context, nodeID uuid.UUID, profile *RepairReferenceProfile) (*repairPlan, error) {
	preparedProfile, err := prepareReferenceProfile(ctx, profile)
	if err != nil {
		return nil, err
	}
	view, err := c.reader.Get(ctx, nodeID)
	if err != nil {
		return nil, loggedRepairError(ctx, fmt.Sprintf("get repair node %s", nodeID), err)
	}
	if view == nil {
		return nil, domain.ErrNotFound
	}
	metadata, err := c.loadReferenceRepairMetadata(ctx, view, preparedProfile, false)
	if err != nil {
		return nil, err
	}
	preview := newReferencePreview(view, preparedProfile, metadata.targetType)
	matchContext := referenceMatchContext{reader: c.reader, typeIndex: metadata.typeIndex, orgID: view.OrgID, scopeID: metadata.scopeID, targetType: metadata.targetType, rankProperty: preparedProfile.RankProperty, matchProperty: preparedProfile.TargetMatchProperty}
	candidates := collectReferenceCandidates(ctx, view, preparedProfile, metadata.targetDef, matchContext, &preview.ObservedSources)
	preview.Candidates = candidates
	chosen, blockReason := chooseReferenceCandidate(candidates, preparedProfile.ConflictPolicy)
	if blockReason != "" {
		preview.NeedsRepair = referenceSourcesPresent(preview.ObservedSources)
		preview.Summary = blockReason
		return &repairPlan{
			preview:             preview,
			props:               nil,
			relationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
		}, nil
	}
	preview.ChosenCandidate = chosen
	props := plannedReferenceProps(view, preparedProfile, chosen.NodeID)
	preview.PlannedProps = props
	preview.NeedsRepair = len(props) > 0
	preview.CanApply = preview.NeedsRepair
	preview.Summary = summarizeReferencePreview(preview)
	if preview.CanApply {
		preview.ConfirmationToken = repairConfirmationToken(preview)
	}
	return &repairPlan{
		preview:             preview,
		props:               props,
		relationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
	}, nil
}

func (c *RepairConsole) loadReferenceRepairMetadata(
	ctx context.Context,
	view *node.NodeView,
	profile RepairReferenceProfile,
	allowExplicitTargetType bool,
) (*referenceRepairMetadata, error) {
	types, err := c.nodeTypes.List(ctx, view.OrgID)
	if err != nil {
		return nil, loggedRepairError(ctx, fmt.Sprintf("list node types for repair %s", view.ID), err)
	}
	typeIndex := node.BuildTypeIndex(types)
	currentType := typeIndex[view.NodeType]
	if currentType == nil {
		return nil, loggedRepairError(ctx, fmt.Sprintf("node type %q for repair %s", view.NodeType, view.ID), domain.ErrNotFound)
	}
	if allowExplicitTargetType && strings.TrimSpace(profile.TargetTypeKey) != "" {
		targetType := typeIndex[strings.TrimSpace(profile.TargetTypeKey)]
		if targetType == nil {
			return nil, loggedRepairError(ctx, fmt.Sprintf("reference target type %q for repair %s", profile.TargetTypeKey, view.ID), domain.ErrNotFound)
		}
		return &referenceRepairMetadata{typeIndex: typeIndex, targetDef: nil, targetType: targetType, scopeID: repairScopeID(view, profile.ScopeFields)}, nil
	}
	defs, err := c.propertyDefs.List(ctx, view.OrgID)
	if err != nil {
		return nil, loggedRepairError(ctx, fmt.Sprintf("list property defs for repair %s", view.ID), err)
	}
	targetDef, err := referenceTargetDef(ctx, defs, currentType, profile.TargetProperty)
	if err != nil {
		return nil, err
	}
	targetType := typeIndex[targetDef.ReferenceTargetTypeKey]
	if targetType == nil {
		return nil, loggedRepairError(ctx, fmt.Sprintf("reference target type %q for repair %s", targetDef.ReferenceTargetTypeKey, view.ID), domain.ErrNotFound)
	}
	return &referenceRepairMetadata{typeIndex: typeIndex, targetDef: targetDef, targetType: targetType, scopeID: repairScopeID(view, profile.ScopeFields)}, nil
}

func referenceTargetDef(ctx context.Context, defs []*node.PropertyDef, currentType *node.NodeType, targetProperty string) (*node.PropertyDef, error) {
	for _, def := range defs {
		if def.Name != targetProperty || !service.PropertyDefAppliesToCommand(def, currentType) {
			continue
		}
		if def.Type != node.PropertyTypeUUID || strings.TrimSpace(def.ReferenceTargetTypeKey) == "" {
			return nil, loggedRepairError(ctx, fmt.Sprintf("reference property %q has invalid definition", targetProperty), domain.ErrFailedPrecondition)
		}
		return def, nil
	}
	return nil, loggedRepairError(ctx, fmt.Sprintf("reference property %q is not declared for node type %q", targetProperty, currentType.TypeKey), domain.ErrNotFound)
}

type referenceRepairMetadata struct {
	typeIndex  map[string]*node.NodeType
	targetDef  *node.PropertyDef
	targetType *node.NodeType
	scopeID    uuid.UUID
}
