//go:build fdb

package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	domain "goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/workspace"
)

// WorkspaceFDBStore implements workspace.Repository using FoundationDB.
//
// Workspace nodes are stored in the node system with:
//   - OrgID = ws.OrgID
//   - WorkspaceID = uuid.Nil
//   - NodeType = "workspace"
//
// Secondary indexes:
//   - (workspace_by_slug, slug) → wsID bytes (globally unique, not org-scoped)
//
// ListForUser requires SQL to query org_members, so this store needs both FDB and a SQL pool.
type WorkspaceFDBStore struct {
	db       fdb.Database
	sqlPool  *pgxpool.Pool
	entities *EntityStore
	views    *ViewStore
}

func NewWorkspaceFDBStore(db fdb.Database, sqlPool *pgxpool.Pool) *WorkspaceFDBStore {
	return &WorkspaceFDBStore{
		db:       db,
		sqlPool:  sqlPool,
		entities: NewEntityStore(db),
		views:    NewViewStore(db),
	}
}

// Create creates a new workspace, generates a UUIDv7 if needed, and writes it atomically.
func (s *WorkspaceFDBStore) Create(ctx context.Context, w *workspace.Workspace) (*workspace.Workspace, error) {
	// Generate UUIDv7 if not set
	if w.ID == uuid.Nil {
		w.ID = uuid.New()
	}

	now := time.Now().UTC()
	w.CreatedAt = now
	w.UpdatedAt = now

	// Generate a synthetic node ID (typically the workspace ID itself for structural entities)
	w.NodeID = w.ID

	// Build the NodeValue for storage in FDB
	nv := &node.NodeValue{
		ID:          w.ID,
		OrgID:       w.OrgID,
		WorkspaceID: uuid.Nil, // workspace is a structural entity at the workspace level
		NodeType:    "workspace",
		Name:        w.Name,
		Description: "", // workspace has no description in the basic model
		CreatedBy:   uuid.Nil,
		UpdatedBy:   uuid.Nil,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Build the NodeListView for list operations
	view := &node.NodeListView{
		ID:          w.ID,
		OrgID:       w.OrgID,
		WorkspaceID: uuid.Nil,
		NodeType:    "workspace",
		Name:        w.Name,
		CreatedAt:   now,
		UpdatedAt:   now,
		CreatedBy:   uuid.Nil,
		UpdatedBy:   uuid.Nil,
		CustomProps: map[string]json.RawMessage{
			"slug": []byte(`"` + w.Slug + `"`), // JSON-encoded slug string
		},
	}

	// Write everything atomically via the entity store
	_, err := s.entities.CreateAtomic(ctx, w.OrgID, uuid.Nil, nv, nil, view, nil, nil, uuid.Nil)
	if err != nil {
		return nil, fmt.Errorf("workspace create atomic: %w", err)
	}

	// Write the slug index in a separate transaction
	err = s.writeWorkspaceSlugIndex(ctx, w.Slug, w.ID)
	if err != nil {
		return nil, fmt.Errorf("workspace write slug index: %w", err)
	}

	return w, nil
}

// GetByID retrieves a workspace by its ID using the ViewStore.
func (s *WorkspaceFDBStore) GetByID(ctx context.Context, id uuid.UUID) (*workspace.Workspace, error) {
	view, err := s.views.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("workspace get by id: %w", err)
	}
	if view == nil {
		return nil, domain.ErrNotFound
	}

	return nodeListViewToWorkspace(view), nil
}

// GetBySlug retrieves a workspace by its slug using the secondary index.
// Workspace slugs are assumed to be globally unique.
func (s *WorkspaceFDBStore) GetBySlug(ctx context.Context, slug string) (*workspace.Workspace, error) {
	key := workspaceBySlugGlobalKey(slug).Pack()

	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(key)).Get()
	})
	if err != nil {
		return nil, fmt.Errorf("workspace slug lookup: %w", err)
	}

	b, ok := val.([]byte)
	if !ok || len(b) == 0 {
		return nil, domain.ErrNotFound
	}

	// Unmarshal the wsID from the stored bytes
	var wsID uuid.UUID
	if err := wsID.UnmarshalBinary(b); err != nil {
		return nil, fmt.Errorf("unmarshal workspace id from slug index: %w", err)
	}

	// Fetch the actual workspace via GetByID
	return s.GetByID(ctx, wsID)
}

// List returns all workspaces in a given org by range-scanning the node_instance prefix.
func (s *WorkspaceFDBStore) List(ctx context.Context, orgID uuid.UUID) ([]*workspace.Workspace, error) {
	// Range scan: (node_instance, orgID, uuid.Nil, "workspace", *) → all workspaces in this org
	prefix := tuple.Tuple{keyNodeInstance, orgID.String(), uuid.Nil.String(), "workspace"}.Pack()
	pr, err := fdb.PrefixRange(prefix)
	if err != nil {
		return nil, fmt.Errorf("workspace list prefix range: %w", err)
	}

	kvs, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.GetRange(pr, fdb.RangeOptions{}).GetSliceWithError()
	})
	if err != nil {
		return nil, fmt.Errorf("workspace list scan: %w", err)
	}

	// Extract workspace IDs from the result keys
	workspaceKVs := kvs.([]fdb.KeyValue)
	workspaceIDs := extractLastUUIDs(workspaceKVs, 4) // (prefix, orgID, wsID=nil, type, wsID)

	if len(workspaceIDs) == 0 {
		return nil, nil
	}

	// Batch fetch all workspace NodeValues
	nvs, err := s.entities.batchGet(orgID, uuid.Nil, "workspace", workspaceIDs)
	if err != nil {
		return nil, fmt.Errorf("workspace list batch get: %w", err)
	}

	// Convert NodeValues to workspace.Workspace entities
	workspaces := make([]*workspace.Workspace, 0, len(nvs))
	for _, nv := range nvs {
		if nv == nil {
			continue
		}
		// Fetch the view to get CustomProps (which contains slug)
		view, err := s.views.Get(ctx, nv.ID)
		if err != nil {
			continue // skip on error
		}
		if view != nil {
			workspaces = append(workspaces, nodeListViewToWorkspace(view))
		}
	}

	return workspaces, nil
}

// ListForUser returns all workspaces the given user has access to via org membership.
// This requires a SQL query to org_members.
func (s *WorkspaceFDBStore) ListForUser(ctx context.Context, userID uuid.UUID) ([]*workspace.Workspace, error) {
	if s.sqlPool == nil {
		return nil, fmt.Errorf("workspace list for user: sql pool not configured")
	}

	// Query SQL to get all orgs the user is a member of
	const q = `SELECT DISTINCT org_id FROM org_members WHERE user_id = $1`
	rows, err := s.sqlPool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("workspace list for user query: %w", err)
	}
	defer rows.Close()

	var orgIDs []uuid.UUID
	for rows.Next() {
		var orgID uuid.UUID
		if err := rows.Scan(&orgID); err != nil {
			return nil, fmt.Errorf("workspace list for user scan: %w", err)
		}
		orgIDs = append(orgIDs, orgID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workspace list for user rows error: %w", err)
	}

	if len(orgIDs) == 0 {
		return nil, nil
	}

	// For each org, list workspaces and merge
	var allWorkspaces []*workspace.Workspace
	for _, orgID := range orgIDs {
		workspaces, err := s.List(ctx, orgID)
		if err != nil {
			// Continue on error for one org; don't fail the entire operation
			continue
		}
		allWorkspaces = append(allWorkspaces, workspaces...)
	}

	return allWorkspaces, nil
}

// writeWorkspaceSlugIndex writes the global secondary index entry for workspace slug lookup.
func (s *WorkspaceFDBStore) writeWorkspaceSlugIndex(ctx context.Context, slug string, wsID uuid.UUID) error {
	key := workspaceBySlugGlobalKey(slug).Pack()
	wsIDBytes, _ := wsID.MarshalBinary()

	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(fdb.Key(key), wsIDBytes)
		return nil, nil
	})
	if err != nil {
		return fmt.Errorf("write workspace slug index: %w", err)
	}
	return nil
}

// workspaceBySlugGlobalKey returns the secondary index key for workspace slug lookup (globally unique).
// (workspace_by_slug_global, slug) → wsID bytes
func workspaceBySlugGlobalKey(slug string) tuple.Tuple {
	return tuple.Tuple{keyWorkspaceBySlugGlobal, slug}
}

// nodeListViewToWorkspace converts a NodeListView into a Workspace entity.
func nodeListViewToWorkspace(view *node.NodeListView) *workspace.Workspace {
	if view == nil {
		return nil
	}

	// Extract slug from CustomProps if present
	slug := ""
	if view.CustomProps != nil {
		if slugBytes, ok := view.CustomProps["slug"]; ok {
			// Parse JSON string
			if err := json.Unmarshal(slugBytes, &slug); err != nil {
				// Fall back to empty slug if unmarshaling fails
				slug = ""
			}
		}
	}

	return &workspace.Workspace{
		ID:        view.ID,
		NodeID:    view.ID,
		OrgID:     view.OrgID,
		Name:      view.Name,
		Slug:      slug,
		CreatedAt: view.CreatedAt,
		UpdatedAt: view.UpdatedAt,
	}
}
