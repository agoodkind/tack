package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
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
	log := telemetry.L(ctx)
	log.Info("node.Create",
		slog.String("node_type", nodeTypeName),
		slog.String("name", name),
		slog.String("parent_id", parentID.String()),
	)

	// Resolve parent to get org/workspace/project context.
	resolve, err := s.reader.Resolve(ctx, parentID)
	if err != nil {
		log.Warn("node.Create: resolve parent failed",
			slog.String("parent_id", parentID.String()),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("resolve parent: %w", err)
	}
	orgID := resolve.OrgID
	wsID := resolve.WorkspaceID
	projID := resolve.ProjectID

	// Determine scoping from the parent's resolve record, not its type name.
	// A scope-level node's own ID fills the first nil scope slot above it.
	if wsID == uuid.Nil {
		// Parent has no workspace scope -- parent IS the workspace-level node.
		wsID = parentID
		projID = uuid.Nil
	} else if projID == uuid.Nil {
		// Parent has workspace scope but no project scope. If the parent is
		// itself a scope node (FeatureIsScope), it IS the project-level node.
		parentNT, _ := s.findNodeType(ctx, orgID, resolve.NodeType)
		if parentNT != nil && parentNT.Features.Has(node.FeatureIsScope) {
			projID = parentID
		}
	}
	// Otherwise: parent is deeper in the hierarchy; inherit wsID and projID as-is.

	log.Debug("node.Create: resolved parent",
		slog.String("org_id", orgID.String()),
		slog.String("workspace_id", wsID.String()),
		slog.String("project_id", projID.String()),
		slog.String("parent_type", resolve.NodeType),
	)

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
		log.Error("node.Create: CreateAtomic failed",
			slog.String("node_type", nodeTypeName),
			slog.String("node_id", id.String()),
			slog.String("err", err.Error()),
		)
		return nil, fmt.Errorf("create node: %w", err)
	}
	view.SequenceID = int32(seqID)
	log.Info("node.Create: created",
		slog.String("node_id", id.String()),
		slog.String("node_type", nodeTypeName),
		slog.Int("sequence_id", int(seqID)),
		slog.String("project_id", projID.String()),
	)

	// HasSlug: write global slug index.
	if nt.Features.Has(node.FeatureHasSlug) {
		for _, slugProp := range []string{"slug", "identifier"} {
			if raw, ok := customProps[slugProp]; ok {
				var slugVal string
				if err := json.Unmarshal(raw, &slugVal); err != nil {
					log.Warn("node.Create: unmarshal slug property", slog.String("prop", slugProp), slog.String("err", err.Error()))
				}
				if slugVal != "" {
					if err := s.entities.WriteSlugIndex(ctx, nt.TypeKey, slugVal, id); err != nil {
						log.Warn("node.Create: write slug index", slog.String("slug", slugVal), slog.String("err", err.Error()))
					}
				}
			}
		}
	}

	// HasActivity: log creation event.
	if nt.Features.Has(node.FeatureHasActivity) {
		if err := s.activity.Append(ctx, orgID, wsID, &node.ActivityEvent{
			EventID: uuid.New(), NodeID: id, Actor: actorID,
			Verb: "created", Detail: map[string]any{"name": name},
			CreatedAt: now,
		}); err != nil {
			log.Warn("node.Create: activity append", slog.String("err", err.Error()))
		}
	}

	// Search index.
	if err := s.searcher.Index(ctx, "nodes", id.String(), nodeDocFromView(view)); err != nil {
		log.Warn("node.Create: search index", slog.String("err", err.Error()))
	}

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
	log := telemetry.L(ctx)
	log.Info("node.Update", slog.String("node_id", nodeID.String()))

	view, err := s.reader.Get(ctx, nodeID)
	if err != nil {
		log.Warn("node.Update: get failed", slog.String("node_id", nodeID.String()), slog.String("err", err.Error()))
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
		ProjectID: view.ProjectID, NodeType: view.NodeType,
		Name: view.Name, StateID: view.StateID,
		CreatedBy: view.CreatedBy, UpdatedBy: actorID,
		CreatedAt: view.CreatedAt, UpdatedAt: now,
	}

	if err := s.entities.Set(ctx, nv, props, view); err != nil {
		log.Error("node.Update: Set failed", slog.String("node_id", nodeID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("update node: %w", err)
	}

	// HasAssignees: update assignments if provided.
	if assigneeIDs != nil && nt.Features.Has(node.FeatureHasAssignees) {
		if err := s.assignments.SetAll(ctx, orgID, nodeID, *assigneeIDs, actorID); err != nil {
			log.Warn("node.Update: set assignments", slog.String("node_id", nodeID.String()), slog.String("err", err.Error()))
		}
		view.AssigneeIDs = *assigneeIDs
	}

	// Labels: update if provided.
	if labelIDs != nil {
		if err := s.labels.SetAll(ctx, orgID, nodeID, *labelIDs, actorID); err != nil {
			log.Warn("node.Update: set labels", slog.String("node_id", nodeID.String()), slog.String("err", err.Error()))
		}
		view.LabelIDs = *labelIDs
	}

	// HasActivity: log update event.
	if nt.Features.Has(node.FeatureHasActivity) {
		if err := s.activity.Append(ctx, orgID, wsID, &node.ActivityEvent{
			EventID: uuid.New(), NodeID: nodeID, Actor: actorID,
			Verb: "updated", CreatedAt: now,
		}); err != nil {
			log.Warn("node.Update: activity append", slog.String("err", err.Error()))
		}
	}

	// Search index.
	if err := s.searcher.Index(ctx, "nodes", nodeID.String(), nodeDocFromView(view)); err != nil {
		log.Warn("node.Update: search index", slog.String("err", err.Error()))
	}

	return view, nil
}

// Delete removes a node.
func (s *NodeService) Delete(ctx context.Context, nodeID, actorID uuid.UUID) error {
	log := telemetry.L(ctx)
	log.Info("node.Delete", slog.String("node_id", nodeID.String()))

	// Read the view to get correct ProjectID (resolve records may have stale ProjectID).
	view, err := s.reader.Get(ctx, nodeID)
	if err != nil {
		log.Warn("node.Delete: get failed", slog.String("node_id", nodeID.String()), slog.String("err", err.Error()))
		return err
	}
	if view == nil {
		return domain.ErrNotFound
	}

	nv := &node.NodeValue{
		ID: nodeID, OrgID: view.OrgID, WorkspaceID: view.WorkspaceID,
		ProjectID: view.ProjectID, NodeType: view.NodeType,
	}
	orgID := view.OrgID

	if err := s.entities.Delete(ctx, nv); err != nil {
		log.Error("node.Delete: failed", slog.String("node_id", nodeID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("delete node: %w", err)
	}

	// Cascade: clean up assignments, labels, comments, etc.
	if err := s.deleter.DeleteNode(ctx, orgID, nodeID); err != nil {
		log.Warn("node.Delete: cascade cleanup", slog.String("node_id", nodeID.String()), slog.String("err", err.Error()))
	}

	// Search removal.
	if err := s.searcher.Delete(ctx, "nodes", nodeID.String()); err != nil {
		log.Warn("node.Delete: search removal", slog.String("node_id", nodeID.String()), slog.String("err", err.Error()))
	}

	return nil
}

// SetState transitions a node to a new workflow state.
func (s *NodeService) SetState(ctx context.Context, nodeID, stateID, actorID uuid.UUID) (*node.NodeListView, error) {
	return s.Update(ctx, nodeID, nil, map[string]any{"state_id": stateID.String()}, nil, nil, actorID)
}

// AddChildren adds child nodes to a container (e.g. issues to a cycle/module/epic).
func (s *NodeService) AddChildren(ctx context.Context, containerID uuid.UUID, childIDs []uuid.UUID, actorID uuid.UUID) error {
	log := telemetry.L(ctx)
	log.Info("node.AddChildren",
		slog.String("container_id", containerID.String()),
		slog.Int("child_count", len(childIDs)),
	)

	resolve, err := s.reader.Resolve(ctx, containerID)
	if err != nil {
		log.Warn("node.AddChildren: resolve failed", slog.String("container_id", containerID.String()), slog.String("err", err.Error()))
		return err
	}

	for _, childID := range childIDs {
		if err := s.containment.AddChild(ctx, resolve.OrgID, containerID, childID, actorID); err != nil {
			log.Warn("node.AddChildren: add child", slog.String("child_id", childID.String()), slog.String("err", err.Error()))
		}
	}
	return nil
}

// RemoveChildren removes child nodes from a container.
func (s *NodeService) RemoveChildren(ctx context.Context, containerID uuid.UUID, childIDs []uuid.UUID) error {
	log := telemetry.L(ctx)
	log.Info("node.RemoveChildren",
		slog.String("container_id", containerID.String()),
		slog.Int("child_count", len(childIDs)),
	)

	resolve, err := s.reader.Resolve(ctx, containerID)
	if err != nil {
		log.Warn("node.RemoveChildren: resolve failed", slog.String("container_id", containerID.String()), slog.String("err", err.Error()))
		return err
	}

	for _, childID := range childIDs {
		if err := s.containment.RemoveChild(ctx, resolve.OrgID, containerID, childID); err != nil {
			log.Warn("node.RemoveChildren: remove child", slog.String("child_id", childID.String()), slog.String("err", err.Error()))
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
