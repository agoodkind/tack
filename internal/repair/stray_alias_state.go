package repair

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func (c *RepairConsole) planStrayAliasState(ctx context.Context, nodeID uuid.UUID) (*repairPlan, error) {
	view, err := c.reader.Get(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, domain.ErrNotFound
	}
	preview := &RepairPreview{
		Class:            RepairClassStrayAliasState,
		NodeID:           nodeID,
		NodeType:         view.NodeType,
		CurrentUpdatedAt: view.UpdatedAt,
		RawState:         stringProp(view.Props, "state"),
		CanonicalStateID: rawUUIDProp(view.Props, "state_id"),
	}
	if strings.TrimSpace(preview.RawState) == "" {
		preview.Summary = "raw state alias is absent"
		return &repairPlan{preview: preview}, nil
	}
	workflowContext, err := c.loadWorkflowContext(ctx, view)
	if err != nil {
		return nil, err
	}
	if workflowContext == nil {
		preview.Summary = "node type has no applicable state_id workflow property"
		return &repairPlan{preview: preview}, nil
	}
	rawState, rawErr := workflowContext.resolveStateInput(ctx, preview.RawState)
	canonicalInput := stringProp(view.Props, "state_id")
	canonicalState, canonicalErr := workflowContext.resolveStateInput(ctx, canonicalInput)
	if rawState != nil {
		preview.ResolvedRawStateID = rawState.ID
	}
	if canonicalState != nil {
		preview.ResolvedCanonicalStateID = canonicalState.ID
	}
	winner := chooseWorkflowWinner(rawState, canonicalState)
	preview.NeedsRepair = true
	if winner == nil {
		preview.CanApply = false
		preview.Summary = summarizeStateRepairFailure(rawErr, canonicalErr)
		return &repairPlan{preview: preview}, nil
	}
	preview.CanApply = true
	preview.WinnerStateID = winner.ID
	preview.WinnerStateName = winner.Name
	preview.ConfirmationToken = repairConfirmationToken(preview, canonicalInput)
	preview.Summary = summarizeStateRepairSuccess(preview, rawState, canonicalState)
	return &repairPlan{
		preview: preview,
		props: map[string]json.RawMessage{
			"state":    json.RawMessage("null"),
			"state_id": service.MustRawString(winner.ID.String()),
		},
	}, nil
}

type workflowRepairContext struct {
	orgID     uuid.UUID
	scopeID   uuid.UUID
	stateType *node.NodeType
	states    []*workflowStateCandidate
	reader    node.NodeReader
}

type workflowStateCandidate struct {
	ID   uuid.UUID
	Name string
	Rank int64
}

func (c *RepairConsole) loadWorkflowContext(ctx context.Context, view *node.NodeView) (*workflowRepairContext, error) {
	types, err := c.nodeTypes.List(ctx, view.OrgID)
	if err != nil {
		return nil, fmt.Errorf("list node types for repair %s: %w", view.ID, err)
	}
	typeIndex := node.BuildTypeIndex(types)
	currentType := typeIndex[view.NodeType]
	if currentType == nil {
		return nil, fmt.Errorf("node type %q for repair %s: %w", view.NodeType, view.ID, domain.ErrNotFound)
	}
	stateDef, err := c.workflowStateProperty(ctx, view.OrgID, currentType)
	if err != nil {
		return nil, err
	}
	if stateDef == nil {
		return nil, nil
	}
	scopeID := rawUUIDProp(view.Props, "scope_id")
	if scopeID == uuid.Nil {
		scopeID = rawUUIDProp(view.Props, "parent_id")
	}
	if scopeID == uuid.Nil {
		return nil, fmt.Errorf("repair %s scope_id missing: %w", view.ID, domain.ErrFailedPrecondition)
	}
	stateType := typeIndex[stateDef.ReferenceTargetTypeKey]
	if stateType == nil {
		return nil, fmt.Errorf("workflow state type %q for repair %s: %w", stateDef.ReferenceTargetTypeKey, view.ID, domain.ErrNotFound)
	}
	stateViews, err := c.reader.List(ctx, node.NodeListQuery{
		OrgID:    view.OrgID,
		NodeType: stateType.TypeKey,
		ByProperty: &node.PropertyMatch{
			PropName: "parent_id",
			Value:    service.MustRawString(scopeID.String()),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("list workflow states for scope %s: %w", scopeID, err)
	}
	states := make([]*workflowStateCandidate, 0, len(stateViews))
	for _, stateView := range stateViews {
		states = append(states, &workflowStateCandidate{
			ID:   stateView.ID,
			Name: stateView.Name,
			Rank: int64Prop(stateView.Props, "sort_order"),
		})
	}
	return &workflowRepairContext{orgID: view.OrgID, scopeID: scopeID, stateType: stateType, states: states, reader: c.reader}, nil
}

func (c *RepairConsole) workflowStateProperty(ctx context.Context, orgID uuid.UUID, currentType *node.NodeType) (*node.PropertyDef, error) {
	defs, err := c.propertyDefs.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list property defs for workflow repair: %w", err)
	}
	for _, def := range defs {
		if def.Name != "state_id" {
			continue
		}
		if !service.PropertyDefAppliesToCommand(def, currentType) {
			continue
		}
		if def.Type != node.PropertyTypeUUID || strings.TrimSpace(def.ReferenceTargetTypeKey) == "" {
			return nil, fmt.Errorf("workflow property %q has invalid definition: %w", def.Name, domain.ErrFailedPrecondition)
		}
		return def, nil
	}
	return nil, nil
}
