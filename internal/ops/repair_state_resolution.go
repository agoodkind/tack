package ops

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

func (c *workflowRepairContext) resolveStateInput(ctx context.Context, input string) (*workflowStateCandidate, error) {
	trimmedInput := strings.TrimSpace(input)
	if trimmedInput == "" {
		return nil, fmt.Errorf("state reference is empty: %w", domain.ErrInvalidArgument)
	}
	command, err := service.CompileUpdatePropertyCommand(
		ctx,
		[]*node.PropertyDef{{Name: "state_id", Type: node.PropertyTypeUUID, ReferenceTargetTypeKey: c.stateType.TypeKey}},
		map[string]*node.NodeType{
			c.stateType.TypeKey: c.stateType,
			"repair_target": &node.NodeType{
				TypeKey:  "repair_target",
				Features: node.Features{node.FeatureHasWorkflowStates},
			},
		},
		&node.NodeType{TypeKey: "repair_target", Features: node.Features{node.FeatureHasWorkflowStates}},
		c.orgID,
		c.scopeID,
		map[string]json.RawMessage{"state": service.MustRawString(trimmedInput)},
		c.resolveReference,
	)
	if err != nil {
		return nil, err
	}
	stateID := rawUUIDProp(command.Props, "state_id")
	if stateID == uuid.Nil {
		return nil, fmt.Errorf("state reference %q resolved to empty state_id: %w", trimmedInput, domain.ErrInvalidArgument)
	}
	for _, candidate := range c.states {
		if candidate.ID == stateID {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("workflow state %q is outside scope %s: %w", trimmedInput, c.scopeID, domain.ErrInvalidArgument)
}

func (c *workflowRepairContext) resolveReference(
	ctx context.Context,
	orgID uuid.UUID,
	scopeID uuid.UUID,
	targetType *node.NodeType,
	ref string,
) (uuid.UUID, error) {
	if targetType == nil || targetType.TypeKey != c.stateType.TypeKey {
		return uuid.Nil, fmt.Errorf("workflow repair target type mismatch: %w", domain.ErrInvalidArgument)
	}
	if orgID != c.orgID || scopeID != c.scopeID {
		return uuid.Nil, fmt.Errorf("workflow repair scope mismatch: %w", domain.ErrInvalidArgument)
	}
	if parsedID, err := uuid.Parse(ref); err == nil {
		for _, candidate := range c.states {
			if candidate.ID == parsedID {
				return parsedID, nil
			}
		}
		return uuid.Nil, fmt.Errorf("workflow state %s is outside scope %s: %w", parsedID, c.scopeID, domain.ErrInvalidArgument)
	}
	localRef := strings.TrimSpace(ref)
	if scopeRef, scopedRef, ok := strings.Cut(localRef, "::"); ok {
		scopeView, err := c.reader.Get(ctx, c.scopeID)
		if err != nil {
			return uuid.Nil, err
		}
		if scopeView == nil {
			return uuid.Nil, fmt.Errorf("workflow scope %s: %w", c.scopeID, domain.ErrNotFound)
		}
		if !matchesReferenceProperty(scopeView, strings.TrimSpace(scopeRef)) {
			return uuid.Nil, fmt.Errorf("state reference %q does not match scope %s: %w", ref, c.scopeID, domain.ErrInvalidArgument)
		}
		localRef = strings.TrimSpace(scopedRef)
	}
	return matchWorkflowState(c.states, localRef)
}

func chooseWorkflowWinner(rawState *workflowStateCandidate, canonicalState *workflowStateCandidate) *workflowStateCandidate {
	if rawState == nil {
		return canonicalState
	}
	if canonicalState == nil {
		return rawState
	}
	if rawState.Rank > canonicalState.Rank {
		return rawState
	}
	if canonicalState.Rank > rawState.Rank {
		return canonicalState
	}
	return canonicalState
}

func matchesReferenceProperty(view *node.NodeView, input string) bool {
	if view == nil {
		return false
	}
	if strings.EqualFold(stringProp(view.Props, "identifier"), input) {
		return true
	}
	if strings.EqualFold(view.Name, input) {
		return true
	}
	return strings.EqualFold(stringProp(view.Props, "slug"), input)
}

func matchWorkflowState(states []*workflowStateCandidate, input string) (uuid.UUID, error) {
	matches := map[uuid.UUID]struct{}{}
	for _, candidate := range states {
		if strings.EqualFold(candidate.Name, input) {
			matches[candidate.ID] = struct{}{}
		}
	}
	switch len(matches) {
	case 0:
		return uuid.Nil, fmt.Errorf("state %q: %w", input, domain.ErrNotFound)
	case 1:
		for id := range matches {
			return id, nil
		}
	}
	return uuid.Nil, fmt.Errorf("state %q matched multiple nodes: %w", input, domain.ErrInvalidArgument)
}
