//go:build fdb

package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	domain "goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
)

// OrgFDBStore implements org.Repository using FoundationDB for entity data
// and SQL for org_members (which stays in the SQL auth layer).
//
// Org nodes are stored in the node system with:
//   - OrgID = org.ID
//   - WorkspaceID = uuid.Nil
//   - NodeType = "org"
//
// Secondary index for slug lookup:
//   - (org_by_slug, slug) → orgID bytes (16 bytes, raw UUID)
type OrgFDBStore struct {
	db       fdb.Database
	sqlPool  *pgxpool.Pool // for org_members (auth table stays in SQL)
	entities *EntityStore
	views    *ViewStore
}

func NewOrgFDBStore(db fdb.Database, sqlPool *pgxpool.Pool) *OrgFDBStore {
	return &OrgFDBStore{
		db:       db,
		sqlPool:  sqlPool,
		entities: NewEntityStore(db),
		views:    NewViewStore(db),
	}
}

// Create creates a new org, generates a UUIDv7 if ID is nil, and writes it atomically.
func (s *OrgFDBStore) Create(ctx context.Context, o *org.Org) (*org.Org, error) {
	// Generate UUIDv7 if not set
	if o.ID == uuid.Nil {
		o.ID = uuid.New()
	}

	now := time.Now().UTC()
	o.CreatedAt = now
	o.UpdatedAt = now

	// Generate a synthetic node ID (typically the org ID itself for structural entities)
	o.NodeID = o.ID

	// Build the NodeValue for storage in FDB
	nv := &node.NodeValue{
		ID:          o.ID,
		OrgID:       o.ID, // org is the org root
		WorkspaceID: uuid.Nil,
		NodeType:    "org",
		Name:        o.Name,
		Description: "", // org has no description in the basic model
		CreatedBy:   uuid.Nil,
		UpdatedBy:   uuid.Nil,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// Build the NodeListView for list operations
	view := &node.NodeListView{
		ID:          o.ID,
		OrgID:       o.ID,
		WorkspaceID: uuid.Nil,
		NodeType:    "org",
		Name:        o.Name,
		CreatedAt:   now,
		UpdatedAt:   now,
		CreatedBy:   uuid.Nil,
		UpdatedBy:   uuid.Nil,
		CustomProps: map[string]json.RawMessage{
			"slug": []byte(`"` + o.Slug + `"`), // JSON-encoded slug string
		},
	}

	// Write everything atomically via the entity store
	_, err := s.entities.CreateAtomic(ctx, o.ID, uuid.Nil, nv, nil, view, nil, nil, uuid.Nil)
	if err != nil {
		return nil, fmt.Errorf("org create atomic: %w", err)
	}

	// Write the slug index in a separate transaction
	err = s.writeOrgSlugIndex(ctx, o.Slug, o.ID)
	if err != nil {
		return nil, fmt.Errorf("org write slug index: %w", err)
	}

	return o, nil
}

// GetByID retrieves an org by its ID using the ViewStore.
func (s *OrgFDBStore) GetByID(ctx context.Context, id uuid.UUID) (*org.Org, error) {
	view, err := s.views.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("org get by id: %w", err)
	}
	if view == nil {
		return nil, domain.ErrNotFound
	}

	return nodeListViewToOrg(view), nil
}

// GetBySlug retrieves an org by its slug using the secondary index.
func (s *OrgFDBStore) GetBySlug(ctx context.Context, slug string) (*org.Org, error) {
	key := orgBySlugKey(slug)

	val, err := s.db.ReadTransact(func(tr fdb.ReadTransaction) (any, error) {
		return tr.Get(fdb.Key(key)).Get()
	})
	if err != nil {
		return nil, fmt.Errorf("org slug lookup: %w", err)
	}

	b, ok := val.([]byte)
	if !ok || len(b) == 0 {
		return nil, domain.ErrNotFound
	}

	// Unmarshal the orgID from the stored bytes
	var orgID uuid.UUID
	if err := orgID.UnmarshalBinary(b); err != nil {
		return nil, fmt.Errorf("unmarshal org id from slug index: %w", err)
	}

	// Fetch the actual org via GetByID
	return s.GetByID(ctx, orgID)
}

// List is not supported for orgs. The system design assumes one org per deployment.
func (s *OrgFDBStore) List(ctx context.Context) ([]*org.Org, error) {
	return nil, fmt.Errorf("org list: not supported in FDB adapter")
}

// writeOrgSlugIndex writes the secondary index entry for org slug lookup.
func (s *OrgFDBStore) writeOrgSlugIndex(ctx context.Context, slug string, orgID uuid.UUID) error {
	key := orgBySlugKey(slug)
	orgIDBytes, _ := orgID.MarshalBinary()

	_, err := s.db.Transact(func(tr fdb.Transaction) (any, error) {
		tr.Set(fdb.Key(key), orgIDBytes)
		return nil, nil
	})
	if err != nil {
		return fmt.Errorf("write org slug index: %w", err)
	}
	return nil
}

// nodeListViewToOrg converts a NodeListView into an Org entity.
func nodeListViewToOrg(view *node.NodeListView) *org.Org {
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

	return &org.Org{
		ID:        view.ID,
		NodeID:    view.ID,
		Name:      view.Name,
		Slug:      slug,
		CreatedAt: view.CreatedAt,
		UpdatedAt: view.UpdatedAt,
	}
}

// AddMember adds a user to the org by inserting into the org_members SQL table.
// org_members is an auth table and stays in SQL even though orgs move to FDB.
func (s *OrgFDBStore) AddMember(ctx context.Context, m *org.Member) error {
	const q = `
		INSERT INTO org_members (org_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role
		RETURNING id, created_at`
	return s.sqlPool.QueryRow(ctx, q, m.OrgID, m.UserID, m.Role).Scan(&m.ID, &m.CreatedAt)
}

// RemoveMember removes a user from the org in the org_members SQL table.
func (s *OrgFDBStore) RemoveMember(ctx context.Context, orgID, userID uuid.UUID) error {
	const q = `DELETE FROM org_members WHERE org_id = $1 AND user_id = $2`
	tag, err := s.sqlPool.Exec(ctx, q, orgID, userID)
	if err != nil {
		return fmt.Errorf("org remove member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// ListMembers returns all members of the org from the org_members SQL table.
func (s *OrgFDBStore) ListMembers(ctx context.Context, orgID uuid.UUID) ([]*org.Member, error) {
	const q = `SELECT id, org_id, user_id, role, created_at FROM org_members WHERE org_id = $1`
	rows, err := s.sqlPool.Query(ctx, q, orgID)
	if err != nil {
		return nil, fmt.Errorf("org list members: %w", err)
	}
	defer rows.Close()
	var members []*org.Member
	for rows.Next() {
		m := &org.Member{}
		if err := rows.Scan(&m.ID, &m.OrgID, &m.UserID, &m.Role, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("org member scan: %w", err)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}
