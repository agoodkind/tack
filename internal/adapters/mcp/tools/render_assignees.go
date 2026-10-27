package tools

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

// UnassignedText is the assignee field value for a node without an
// assigned_to relationship.
const UnassignedText = "unassigned"

// loadAssignees stores target user IDs on rc for views with has_assignees.
// It groups these views by organization and calls ListBySources once per
// organization with assigned_to. Each get or list response uses one call.
// It reads relationships because NodeReader relation scans cannot return
// user targets.
func (rc *renderCtx) loadAssignees(relationships node.RelationshipBatchLister, views ...*node.NodeView) error {
	sourcesByOrg := map[uuid.UUID][]uuid.UUID{}
	for _, view := range views {
		if rc.hasAssignees(view) {
			sourcesByOrg[view.OrgID] = append(sourcesByOrg[view.OrgID], view.ID)
		}
	}
	for orgID, sourceIDs := range sourcesByOrg {
		edgesBySource, err := relationships.ListBySources(rc.ctx, orgID, sourceIDs, node.RelAssignedTo)
		if err != nil {
			slog.ErrorContext(rc.ctx, "render.assignees_read_failed",
				slog.String("org_id", orgID.String()), slog.Int("node_count", len(sourceIDs)),
				slog.String("err", err.Error()))
			return fmt.Errorf("list assigned_to relationships of %d nodes in org %s: %w", len(sourceIDs), orgID, err)
		}
		for _, sourceID := range sourceIDs {
			edges := edgesBySource[sourceID]
			targets := make([]uuid.UUID, 0, len(edges))
			for _, edge := range edges {
				targets = append(targets, edge.TargetID)
			}
			rc.assignees[sourceID] = targets
		}
	}
	return nil
}

// hasAssignees reports whether the view's NodeType metadata includes
// node.FeatureHasAssignees.
func (rc *renderCtx) hasAssignees(view *node.NodeView) bool {
	if rc == nil || view == nil {
		return false
	}
	nodeType := rc.typeIndex[view.NodeType]
	return nodeType != nil && nodeType.Features.Has(node.FeatureHasAssignees)
}

// loadedAssignees returns the assignee user IDs loadAssignees stored for
// the view. It returns false when the type lacks has_assignees or
// loadAssignees did not load the view.
func (rc *renderCtx) loadedAssignees(view *node.NodeView) ([]uuid.UUID, bool) {
	if !rc.hasAssignees(view) {
		return nil, false
	}
	targets, loaded := rc.assignees[view.ID]
	return targets, loaded
}

// assigneeName returns the user's display name or email from the resolver.
// It returns the user ID in code format when the resolver cannot find
// the user.
func (rc *renderCtx) assigneeName(userID uuid.UUID) string {
	if name := rc.userDisplay(userID); name != "" {
		return name
	}
	return markdownCodeValue(userID.String())
}

// appendNestedAssigned adds the "Assigned" field to a list row when
// loadAssignees loaded the view. It shows "unassigned" for zero assignees,
// the assignee's name for one, or the user count for multiple assignees.
func appendNestedAssigned(fields []markdownField, rc *renderCtx, view *node.NodeView) []markdownField {
	assignees, loaded := rc.loadedAssignees(view)
	switch {
	case !loaded:
		return fields
	case len(assignees) == 0:
		return append(fields, markdownFieldValue("Assigned", UnassignedText))
	case len(assignees) == 1:
		return append(fields, markdownFieldValue("Assigned", rc.assigneeName(assignees[0])))
	default:
		return append(fields, markdownFieldValue("Assigned", fmt.Sprintf("%d users", len(assignees))))
	}
}

// appendAssignees appends an "Assignees" field to fields for a single node
// response only when loadAssignees has loaded the assignees for view.
// The field value lists every assignee's name with commas between names.
// The value is "unassigned" when the node does not have an assigned_to
// relationship.
func appendAssignees(fields []markdownField, rc *renderCtx, view *node.NodeView) []markdownField {
	assignees, loaded := rc.loadedAssignees(view)
	if !loaded {
		return fields
	}
	if len(assignees) == 0 {
		return append(fields, markdownFieldValue("Assignees", UnassignedText))
	}
	names := make([]string, 0, len(assignees))
	for _, userID := range assignees {
		names = append(names, rc.assigneeName(userID))
	}
	return append(fields, markdownFieldValue("Assignees", strings.Join(names, ", ")))
}
