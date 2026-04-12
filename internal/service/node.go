package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	domainsearch "goodkind.io/tack/internal/domain/search"
)

// stringToPropertyValue converts a string input to a typed PropertyValue.
func stringToPropertyValue(val string, propType node.PropertyType) *node.PropertyValue {
	switch propType {
	case node.PropertyTypeText, node.PropertyTypeURL:
		return node.TextPropertyValue(val)
	case node.PropertyTypeSelect:
		return &node.PropertyValue{Kind: node.PropertyValueEnum, Enum: &val}
	case node.PropertyTypeNumber:
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return nil
		}
		return &node.PropertyValue{Kind: node.PropertyValueFloat, Float: &f}
	case node.PropertyTypeCheckbox:
		b := val == "true"
		return &node.PropertyValue{Kind: node.PropertyValueBool, Bool: &b}
	case node.PropertyTypeTimestamp:
		t, err := time.Parse(time.RFC3339, val)
		if err != nil {
			return nil
		}
		return &node.PropertyValue{Kind: node.PropertyValueTimestamp, Timestamp: &t}
	default:
		return node.TextPropertyValue(val)
	}
}

// NodeService handles CRUD for any node type. Behavior is driven by Features
// on the NodeType definition. It never checks type names.
type NodeService struct {
	entities    node.EntityRepository
	reader      node.NodeReader
	nodeTypes   node.TypeRepository
	properties  node.PropertyRepository
	activity    node.ActivityRepository
	assignments node.AssignmentRepository
	labels      node.NodeLabelRepository
	containment node.ContainmentRepository
	deleter     node.NodeDeleter
	cleanup     node.NodeCleanupScheduler
	searcher    domainsearch.Searcher
	automations node.AutomationExecutor
}

func NewNodeService(
	entities node.EntityRepository,
	reader node.NodeReader,
	nodeTypes node.TypeRepository,
	properties node.PropertyRepository,
	activity node.ActivityRepository,
	assignments node.AssignmentRepository,
	labels node.NodeLabelRepository,
	containment node.ContainmentRepository,
	deleter node.NodeDeleter,
	cleanup node.NodeCleanupScheduler,
	searcher domainsearch.Searcher,
	automations node.AutomationExecutor,
) *NodeService {
	return &NodeService{
		entities: entities, reader: reader, nodeTypes: nodeTypes,
		properties: properties, activity: activity, assignments: assignments,
		labels: labels, containment: containment, deleter: deleter,
		cleanup: cleanup, searcher: searcher, automations: automations,
	}
}

// Create creates a node of any type. Feature-driven behavior:
//   - HasSequenceID: allocates sequence number (via CreateAtomic when projectID is set)
//   - HasSlug: writes global slug index from the slug/identifier property
//   - HasActivity: logs a "created" activity event
//   - HasAssignees: sets initial assignees
//
// Properties are passed as name→value. The service resolves PropertyDef UUIDs.
func (s *NodeService) Create(
	ctx context.Context,
	parentID uuid.UUID,
	nodeTypeName string,
	name string,
	inputProps map[string]any,
	assigneeIDs []uuid.UUID,
	labelIDs []uuid.UUID,
	actorID uuid.UUID,
) (*node.NodeListView, error) {
	// Resolve parent to get org/workspace/project context.
	resolve, err := s.reader.Resolve(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("resolve parent: %w", err)
	}
	orgID := resolve.OrgID
	wsID := resolve.WorkspaceID
	projID := resolve.ProjectID

	// Determine the correct scoping based on what the parent is.
	// If parent is a workspace (wsID == uuid.Nil), the new node lives at workspace level.
	// If parent is a project, the new node lives under that project.
	if resolve.NodeType == node.NodeTypeWorkspace {
		wsID = parentID
		projID = uuid.Nil
	} else if resolve.NodeType == node.NodeTypeProject {
		projID = parentID
		// wsID comes from the project's resolve
		wsID = resolve.WorkspaceID
	}

	// Look up the NodeType to get Features.
	nt, err := s.findNodeType(ctx, orgID, nodeTypeName)
	if err != nil {
		return nil, err
	}

	// Resolve PropertyDefs by name.
	defs, _ := s.properties.ListDefs(ctx, orgID, wsID, nil)
	defsByName := make(map[string]*node.PropertyDef, len(defs))
	for _, d := range defs {
		defsByName[strings.ToLower(d.Name)] = d
	}

	now := time.Now()
	id := uuid.New()

	// Build typed property map and CustomProps.
	props := make(map[uuid.UUID]*node.PropertyValue)
	customProps := make(map[string]json.RawMessage)

	// Inline fields for system properties.
	var stateID *uuid.UUID
	var startDate, dueDate *time.Time
	priority := ""
	isDraft := false

	for name, val := range inputProps {
		valStr := fmt.Sprintf("%v", val)
		def, ok := defsByName[strings.ToLower(name)]
		if !ok {
			continue
		}
		pv := stringToPropertyValue(valStr, def.Type)
		if pv != nil {
			props[def.ID] = pv
			raw, _ := json.Marshal(val)
			customProps[name] = raw
		}

		// Map system properties to inline fields.
		switch strings.ToLower(name) {
		case "state_id":
			if sid, err := uuid.Parse(valStr); err == nil {
				stateID = &sid
			}
		case "priority":
			priority = valStr
		case "start_date":
			if t, err := time.Parse(time.RFC3339, valStr); err == nil {
				startDate = &t
			}
		case "due_date", "target_date", "end_date":
			if t, err := time.Parse(time.RFC3339, valStr); err == nil {
				dueDate = &t
			}
		case "is_draft":
			isDraft = valStr == "true"
		}
	}

	nv := &node.NodeValue{
		ID: id, OrgID: orgID, WorkspaceID: wsID, ProjectID: projID,
		NodeType: nt.TypeKey, Name: name, StateID: stateID,
		CreatedBy: actorID, UpdatedBy: actorID, CreatedAt: now, UpdatedAt: now,
	}
	view := &node.NodeListView{
		Version: node.ViewVersion1, ID: id, OrgID: orgID, WorkspaceID: wsID,
		ProjectID: projID, NodeType: nt.TypeKey, Name: name,
		StateID: stateID, Priority: priority, StartDate: startDate, DueDate: dueDate,
		IsDraft: isDraft, CreatedBy: actorID, UpdatedBy: actorID,
		CreatedAt: now, UpdatedAt: now, CustomProps: customProps,
	}

	// CreateAtomic handles sequence allocation when projID is set.
	seqID, err := s.entities.CreateAtomic(ctx, orgID, projID, nv, props, view, assigneeIDs, labelIDs, actorID)
	if err != nil {
		return nil, fmt.Errorf("create node: %w", err)
	}
	view.SequenceID = int32(seqID)

	// HasSlug: write global slug index.
	if nt.Features.Has(node.FeatureHasSlug) {
		for _, slugProp := range []string{"slug", "identifier"} {
			if raw, ok := customProps[slugProp]; ok {
				var slugVal string
				_ = json.Unmarshal(raw, &slugVal)
				if slugVal != "" {
					_ = s.entities.WriteSlugIndex(ctx, nt.TypeKey, slugVal, id)
				}
			}
		}
	}

	// HasActivity: log creation event.
	if nt.Features.Has(node.FeatureHasActivity) {
		_ = s.activity.Append(ctx, orgID, wsID, &node.ActivityEvent{
			EventID: uuid.New(), NodeID: id, Actor: actorID,
			Verb: "created", Detail: map[string]any{"name": name},
			CreatedAt: now,
		})
	}

	// Search index.
	_ = s.searcher.Index(ctx, "nodes", id.String(), nodeDocFromView(view))

	return view, nil
}

// Update updates a node's fields and properties.
func (s *NodeService) Update(
	ctx context.Context,
	nodeID uuid.UUID,
	name *string,
	inputProps map[string]any,
	assigneeIDs *[]uuid.UUID,
	labelIDs *[]uuid.UUID,
	actorID uuid.UUID,
) (*node.NodeListView, error) {
	view, err := s.reader.Get(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, domain.ErrNotFound
	}
	resolve, err := s.reader.Resolve(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	orgID := resolve.OrgID
	wsID := resolve.WorkspaceID

	nt, err := s.findNodeType(ctx, orgID, view.NodeType)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	if name != nil {
		view.Name = *name
	}
	view.UpdatedAt = now
	view.UpdatedBy = actorID

	// Resolve PropertyDefs by name.
	defs, _ := s.properties.ListDefs(ctx, orgID, wsID, nil)
	defsByName := make(map[string]*node.PropertyDef, len(defs))
	for _, d := range defs {
		defsByName[strings.ToLower(d.Name)] = d
	}

	props := make(map[uuid.UUID]*node.PropertyValue)
	if view.CustomProps == nil {
		view.CustomProps = make(map[string]json.RawMessage)
	}
	for propName, val := range inputProps {
		valStr := fmt.Sprintf("%v", val)
		def, ok := defsByName[strings.ToLower(propName)]
		if !ok {
			continue
		}
		pv := stringToPropertyValue(valStr, def.Type)
		if pv != nil {
			props[def.ID] = pv
			raw, _ := json.Marshal(val)
			view.CustomProps[propName] = raw
		}

		// Update inline fields for system properties.
		switch strings.ToLower(propName) {
		case "state_id":
			if sid, err := uuid.Parse(valStr); err == nil {
				view.StateID = &sid
			}
		case "priority":
			view.Priority = valStr
		case "start_date":
			if t, err := time.Parse(time.RFC3339, valStr); err == nil {
				view.StartDate = &t
			}
		case "due_date", "target_date", "end_date":
			if t, err := time.Parse(time.RFC3339, valStr); err == nil {
				view.DueDate = &t
			}
		case "is_draft":
			view.IsDraft = valStr == "true"
		}
	}

	nv := &node.NodeValue{
		ID: nodeID, OrgID: orgID, WorkspaceID: wsID,
		ProjectID: resolve.ProjectID, NodeType: view.NodeType,
		Name: view.Name, StateID: view.StateID,
		CreatedBy: view.CreatedBy, UpdatedBy: actorID,
		CreatedAt: view.CreatedAt, UpdatedAt: now,
	}

	if err := s.entities.Set(ctx, nv, props, view); err != nil {
		return nil, fmt.Errorf("update node: %w", err)
	}

	// HasAssignees: update assignments if provided.
	if assigneeIDs != nil && nt.Features.Has(node.FeatureHasAssignees) {
		_ = s.assignments.SetAll(ctx, orgID, nodeID, *assigneeIDs, actorID)
		view.AssigneeIDs = *assigneeIDs
	}

	// Labels: update if provided.
	if labelIDs != nil {
		_ = s.labels.SetAll(ctx, orgID, nodeID, *labelIDs, actorID)
		view.LabelIDs = *labelIDs
	}

	// HasActivity: log update event.
	if nt.Features.Has(node.FeatureHasActivity) {
		_ = s.activity.Append(ctx, orgID, wsID, &node.ActivityEvent{
			EventID: uuid.New(), NodeID: nodeID, Actor: actorID,
			Verb: "updated", CreatedAt: now,
		})
	}

	// Search index.
	_ = s.searcher.Index(ctx, "nodes", nodeID.String(), nodeDocFromView(view))

	return view, nil
}

// Delete removes a node.
func (s *NodeService) Delete(ctx context.Context, nodeID, actorID uuid.UUID) error {
	resolve, err := s.reader.Resolve(ctx, nodeID)
	if err != nil {
		return err
	}
	orgID := resolve.OrgID

	nv := &node.NodeValue{
		ID: nodeID, OrgID: orgID, WorkspaceID: resolve.WorkspaceID,
		ProjectID: resolve.ProjectID, NodeType: resolve.NodeType,
	}

	if err := s.entities.Delete(ctx, nv); err != nil {
		return fmt.Errorf("delete node: %w", err)
	}

	// Cascade: clean up assignments, labels, comments, etc.
	_ = s.deleter.DeleteNode(ctx, orgID, nodeID)

	// Search removal.
	_ = s.searcher.Delete(ctx, "nodes", nodeID.String())

	return nil
}

// SetState transitions a node to a new workflow state.
func (s *NodeService) SetState(ctx context.Context, nodeID, stateID, actorID uuid.UUID) (*node.NodeListView, error) {
	return s.Update(ctx, nodeID, nil, map[string]any{"state_id": stateID.String()}, nil, nil, actorID)
}

// AddChildren adds child nodes to a container (e.g. issues to a cycle/module).
func (s *NodeService) AddChildren(ctx context.Context, containerID uuid.UUID, childIDs []uuid.UUID, containerType, actorID uuid.UUID) error {
	resolve, err := s.reader.Resolve(ctx, containerID)
	if err != nil {
		return err
	}
	orgID := resolve.OrgID

	// Determine containment type from the container's node type.
	switch resolve.NodeType {
	case node.NodeTypeCycle:
		for _, childID := range childIDs {
			_ = s.containment.AddIssueToCycle(ctx, orgID, containerID, childID, actorID)
		}
	case node.NodeTypeModule:
		for _, childID := range childIDs {
			_ = s.containment.AddIssueToModule(ctx, orgID, containerID, childID, actorID)
		}
	}
	return nil
}

// RemoveChildren removes child nodes from a container.
func (s *NodeService) RemoveChildren(ctx context.Context, containerID uuid.UUID, childIDs []uuid.UUID) error {
	resolve, err := s.reader.Resolve(ctx, containerID)
	if err != nil {
		return err
	}
	orgID := resolve.OrgID

	switch resolve.NodeType {
	case node.NodeTypeCycle:
		for _, childID := range childIDs {
			_ = s.containment.RemoveIssueFromCycle(ctx, orgID, containerID, childID)
		}
	case node.NodeTypeModule:
		for _, childID := range childIDs {
			_ = s.containment.RemoveIssueFromModule(ctx, orgID, containerID, childID)
		}
	}
	return nil
}

// findNodeType looks up a NodeType by slug or TypeKey within an org.
func (s *NodeService) findNodeType(ctx context.Context, orgID uuid.UUID, nameOrKey string) (*node.NodeType, error) {
	nts, err := s.nodeTypes.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list node types: %w", err)
	}
	for _, nt := range nts {
		if nt.TypeKey == nameOrKey || nt.Slug == nameOrKey {
			return nt, nil
		}
	}
	return nil, fmt.Errorf("node type %q: %w", nameOrKey, domain.ErrNotFound)
}
