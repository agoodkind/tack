//go:build fdb

package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	domain "goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/project"
	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
)

type ProjectFDBStore struct {
	db       fdb.Database
	entities *EntityStore
	views    *ViewStore
}

func NewProjectFDBStore(db fdb.Database, entities *EntityStore, views *ViewStore) *ProjectFDBStore {
	return &ProjectFDBStore{
		db:       db,
		entities: entities,
		views:    views,
	}
}

func (s *ProjectFDBStore) Create(ctx context.Context, proj *project.Project) (*project.Project, error) {
	// Generate UUIDv7 if ID is nil
	if proj.ID == uuid.Nil {
		proj.ID = uuid.New()
	}
	if proj.NodeID == uuid.Nil {
		proj.NodeID = uuid.New()
	}

	// Resolve org context from workspace
	wsResolve, err := s.views.Resolve(ctx, proj.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("project create resolve workspace: %w", err)
	}

	orgID := wsResolve.OrgID

	// Build NodeValue
	nv := &node.NodeValue{
		ID:          proj.ID,
		OrgID:       orgID,
		WorkspaceID: proj.WorkspaceID,
		ProjectID:   uuid.Nil,
		NodeType:    node.NodeTypeProject,
		Name:        proj.Name,
		Description: proj.Description,
		CreatedBy:   proj.CreatedBy,
		UpdatedBy:   proj.CreatedBy,
		CreatedAt:   proj.CreatedAt,
		UpdatedAt:   proj.UpdatedAt,
	}

	// Build properties map
	props := make(map[uuid.UUID]*node.PropertyValue)
	if proj.Identifier != "" {
		props[systemPropID(proj.WorkspaceID, propNameIdentifier)] = textPV(proj.Identifier)
	}
	if proj.Network != 0 {
		netStr := fmt.Sprintf("%d", proj.Network)
		props[systemPropID(proj.WorkspaceID, propNameNetwork)] = textPV(netStr)
	}
	if proj.DefaultStateID != nil {
		defStateStr := proj.DefaultStateID.String()
		props[systemPropID(proj.WorkspaceID, propNameDefaultStateID)] = textPV(defStateStr)
	}

	// Build NodeListView
	view := &node.NodeListView{
		Version:     node.ViewVersion1,
		ID:          proj.ID,
		OrgID:       orgID,
		WorkspaceID: proj.WorkspaceID,
		ProjectID:   uuid.Nil,
		NodeType:    node.NodeTypeProject,
		Name:        proj.Name,
		Description: proj.Description,
		CreatedBy:   proj.CreatedBy,
		UpdatedBy:   proj.CreatedBy,
		CreatedAt:   proj.CreatedAt,
		UpdatedAt:   proj.UpdatedAt,
	}

	// Write entity and secondary indexes in a transaction
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		// Write primary node_instance record
		primaryKey := nodeInstanceKey(orgID, proj.WorkspaceID, node.NodeTypeProject, proj.ID)
		nvBytes, err := json.Marshal(nv)
		if err != nil {
			return nil, fmt.Errorf("marshal node value: %w", err)
		}
		tr.Set(fdb.Key(primaryKey), nvBytes)

		// Write project_by_workspace index
		tr.Set(fdb.Key(projectByWorkspaceKey(orgID, proj.WorkspaceID, proj.ID).Pack()), []byte{})

		// Write project_by_identifier index (case-insensitive)
		if proj.Identifier != "" {
			identKey := projectByIdentKey(orgID, proj.WorkspaceID, strings.ToUpper(proj.Identifier)).Pack()
			projIDBytes, _ := proj.ID.MarshalBinary()
			tr.Set(fdb.Key(identKey), projIDBytes)
		}

		// Write properties
		for defID, pv := range props {
			pvBytes, err := json.Marshal(pv)
			if err != nil {
				return nil, fmt.Errorf("marshal property value: %w", err)
			}
			pvKey := tuple.Tuple{keyPropertyValueOnNode, orgID.String(), proj.ID.String(), defID.String()}.Pack()
			tr.Set(fdb.Key(pvKey), pvBytes)
			tr.Set(fdb.Key(nodeByPropertyKey(orgID, proj.WorkspaceID, node.NodeTypeProject, defID, encodeForIndex(pv), proj.ID)), []byte{})
		}

		// Write node_by_project index
		wsBytes, _ := proj.WorkspaceID.MarshalBinary()
		tr.Set(fdb.Key(nodeByProjectKey(orgID, proj.ProjectID, node.NodeTypeProject, proj.ID)), wsBytes)

		// Write resolution record
		resolveBytes, err := json.Marshal(&node.NodeResolve{
			OrgID:       orgID,
			WorkspaceID: proj.WorkspaceID,
			NodeType:    node.NodeTypeProject,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal node resolve: %w", err)
		}
		tr.Set(fdb.Key(nodeResolveKey(proj.ID)), resolveBytes)

		// Write list view
		viewBytes, err := json.Marshal(view)
		if err != nil {
			return nil, fmt.Errorf("marshal node list view: %w", err)
		}
		tr.Set(fdb.Key(nodeListViewKey(orgID, proj.WorkspaceID, node.NodeTypeProject, proj.ID)), viewBytes)

		return nil, nil
	})
	if err != nil {
		return nil, fmt.Errorf("project create transaction: %w", err)
	}

	return proj, nil
}

func (s *ProjectFDBStore) GetByID(ctx context.Context, workspaceID, id uuid.UUID) (*project.Project, error) {
	view, err := s.views.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("project get by id: %w", err)
	}
	if view == nil {
		return nil, domain.ErrNotFound
	}

	return nodeListViewToProject(view), nil
}

func (s *ProjectFDBStore) GetByIdentifier(ctx context.Context, workspaceID uuid.UUID, identifier string) (*project.Project, error) {
	// Resolve org context
	wsResolve, err := s.views.Resolve(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("project get by identifier resolve: %w", err)
	}

	orgID := wsResolve.OrgID

	// Read project_by_identifier index
	identKey := fdb.Key(projectByIdentKey(orgID, workspaceID, strings.ToUpper(identifier)).Pack())
	projIDBytes, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(identKey).Get()
	})
	if err != nil {
		return nil, fmt.Errorf("project get by identifier read: %w", err)
	}
	b, ok := projIDBytes.([]byte)
	if !ok || len(b) == 0 {
		return nil, domain.ErrNotFound
	}

	projID, err := uuid.FromBytes(b)
	if err != nil {
		return nil, fmt.Errorf("project get by identifier unmarshal: %w", err)
	}

	// Get the project by ID
	return s.GetByID(ctx, workspaceID, projID)
}

func (s *ProjectFDBStore) List(ctx context.Context, workspaceID uuid.UUID) ([]*project.Project, error) {
	// Resolve org context
	wsResolve, err := s.views.Resolve(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("project list resolve: %w", err)
	}

	orgID := wsResolve.OrgID

	// Scan project_by_workspace index
	prefix := projectByWorkspaceKey(orgID, workspaceID, uuid.Nil).Pack()
	prefixRange, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, fmt.Errorf("project list prefix range: %w", err)
	}

	indexVals, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(prefixRange, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("project list index scan: %w", err)
	}

	kvs := indexVals.([]fdb.KeyValue)
	if len(kvs) == 0 {
		return nil, nil
	}

	// Extract project IDs from index keys
	var projIDs []uuid.UUID
	for _, kv := range kvs {
		t, err := tuple.Unpack(kv.Key)
		if err != nil || len(t) < 4 {
			continue
		}
		s, ok := t[3].(string)
		if !ok {
			continue
		}
		id, err := uuid.Parse(s)
		if err != nil {
			continue
		}
		projIDs = append(projIDs, id)
	}

	if len(projIDs) == 0 {
		return nil, nil
	}

	// Fetch NodeListView for each project
	var projects []*project.Project
	for _, projID := range projIDs {
		p, err := s.GetByID(ctx, workspaceID, projID)
		if err != nil && err != domain.ErrNotFound {
			return nil, err
		}
		if p != nil {
			projects = append(projects, p)
		}
	}

	return projects, nil
}

func (s *ProjectFDBStore) Update(ctx context.Context, proj *project.Project) (*project.Project, error) {
	// Fetch existing project to get org context
	existing, err := s.GetByID(ctx, proj.WorkspaceID, proj.ID)
	if err != nil {
		return nil, fmt.Errorf("project update fetch: %w", err)
	}

	wsResolve, err := s.views.Resolve(ctx, proj.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("project update resolve: %w", err)
	}
	orgID := wsResolve.OrgID

	// Build updated NodeValue
	nv := &node.NodeValue{
		ID:          proj.ID,
		OrgID:       orgID,
		WorkspaceID: proj.WorkspaceID,
		ProjectID:   uuid.Nil,
		NodeType:    node.NodeTypeProject,
		Name:        proj.Name,
		Description: proj.Description,
		CreatedBy:   proj.CreatedBy,
		UpdatedBy:   proj.UpdatedBy,
		CreatedAt:   proj.CreatedAt,
		UpdatedAt:   proj.UpdatedAt,
	}

	// Build properties
	props := make(map[uuid.UUID]*node.PropertyValue)
	if proj.Identifier != "" {
		props[systemPropID(proj.WorkspaceID, propNameIdentifier)] = textPV(proj.Identifier)
	}
	if proj.Network != 0 {
		netStr := fmt.Sprintf("%d", proj.Network)
		props[systemPropID(proj.WorkspaceID, propNameNetwork)] = textPV(netStr)
	}
	if proj.DefaultStateID != nil {
		defStateStr := proj.DefaultStateID.String()
		props[systemPropID(proj.WorkspaceID, propNameDefaultStateID)] = textPV(defStateStr)
	}

	// Build updated view
	view := &node.NodeListView{
		Version:     node.ViewVersion1,
		ID:          proj.ID,
		OrgID:       orgID,
		WorkspaceID: proj.WorkspaceID,
		ProjectID:   uuid.Nil,
		NodeType:    node.NodeTypeProject,
		Name:        proj.Name,
		Description: proj.Description,
		CreatedBy:   proj.CreatedBy,
		UpdatedBy:   proj.UpdatedBy,
		CreatedAt:   proj.CreatedAt,
		UpdatedAt:   proj.UpdatedAt,
	}

	// Update in transaction, handling identifier index changes
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		// Clear old identifier index if identifier changed
		if existing != nil && existing.Identifier != proj.Identifier {
			oldIdentKey := projectByIdentKey(orgID, proj.WorkspaceID, strings.ToUpper(existing.Identifier)).Pack()
			tr.Clear(fdb.Key(oldIdentKey))
		}

		// Write new identifier index
		if proj.Identifier != "" {
			identKey := projectByIdentKey(orgID, proj.WorkspaceID, strings.ToUpper(proj.Identifier)).Pack()
			projIDBytes, _ := proj.ID.MarshalBinary()
			tr.Set(fdb.Key(identKey), projIDBytes)
		}

		// Use entities.Set to handle standard updates
		return nil, s.entities.Set(ctx, nv, props, view)
	})
	if err != nil {
		return nil, fmt.Errorf("project update transaction: %w", err)
	}

	return proj, nil
}

func (s *ProjectFDBStore) Delete(ctx context.Context, id uuid.UUID) error {
	// Get the project to access org/workspace context
	proj, err := s.GetByID(ctx, uuid.Nil, id)
	if err != nil {
		return fmt.Errorf("project delete fetch: %w", err)
	}

	wsResolve, err := s.views.Resolve(ctx, proj.WorkspaceID)
	if err != nil {
		return fmt.Errorf("project delete resolve: %w", err)
	}
	orgID := wsResolve.OrgID

	// Delete in transaction
	_, err = s.db.Transact(func(tr fdb.Transaction) (any, error) {
		// Clear identifier index
		if proj.Identifier != "" {
			identKey := projectByIdentKey(orgID, proj.WorkspaceID, strings.ToUpper(proj.Identifier)).Pack()
			tr.Clear(fdb.Key(identKey))
		}

		// Clear workspace index
		tr.Clear(fdb.Key(projectByWorkspaceKey(orgID, proj.WorkspaceID, proj.ID).Pack()))

		// Use entities.Delete for standard cleanup
		nv := &node.NodeValue{
			ID:          proj.ID,
			OrgID:       orgID,
			WorkspaceID: proj.WorkspaceID,
			ProjectID:   uuid.Nil,
			NodeType:    node.NodeTypeProject,
		}
		return nil, s.entities.Delete(ctx, nv)
	})
	return err
}

func (s *ProjectFDBStore) AllocateSequenceID(ctx context.Context, projectID uuid.UUID, entityType string) (int, error) {
	// Get org context from project
	projResolve, err := s.views.Resolve(ctx, projectID)
	if err != nil {
		return 0, fmt.Errorf("allocate sequence id resolve: %w", err)
	}

	// Use the sequence allocator
	seqID, err := s.entities.AllocateSequenceID(ctx, projResolve.OrgID, projectID, entityType)
	if err != nil {
		return 0, err
	}
	return int(seqID), nil
}

func nodeListViewToProject(v *node.NodeListView) *project.Project {
	if v == nil {
		return nil
	}

	proj := &project.Project{
		ID:          v.ID,
		NodeID:      v.ID, // In FDB, NodeID and ID are the same
		WorkspaceID: v.WorkspaceID,
		Name:        v.Name,
		Description: v.Description,
		CreatedBy:   v.CreatedBy,
		UpdatedBy:   v.UpdatedBy,
		CreatedAt:   v.CreatedAt,
		UpdatedAt:   v.UpdatedAt,
	}

	// Extract custom properties
	if v.CustomProps != nil {
		if identRaw, ok := v.CustomProps[propNameIdentifier]; ok {
			var s string
			if err := json.Unmarshal(identRaw, &s); err == nil {
				proj.Identifier = s
			}
		}
		if netRaw, ok := v.CustomProps[propNameNetwork]; ok {
			var s string
			if err := json.Unmarshal(netRaw, &s); err == nil {
				fmt.Sscanf(s, "%d", &proj.Network)
			}
		}
		if defStateRaw, ok := v.CustomProps[propNameDefaultStateID]; ok {
			var s string
			if err := json.Unmarshal(defStateRaw, &s); err == nil {
				if id, err := uuid.Parse(s); err == nil {
					proj.DefaultStateID = &id
				}
			}
		}
	}

	return proj
}

func systemPropID(workspaceID uuid.UUID, name string) uuid.UUID {
	tackPropNamespace := uuid.MustParse("7ac0face-dead-beef-cafe-000000000000")
	return uuid.NewSHA1(tackPropNamespace, []byte(workspaceID.String()+":"+name))
}

func textPV(s string) *node.PropertyValue {
	return &node.PropertyValue{Kind: node.PropertyValueText, Text: &s}
}

const (
	propNameIdentifier     = "identifier"
	propNameDescription    = "description"
	propNameNetwork        = "network"
	propNameDefaultStateID = "default_state_id"
)
