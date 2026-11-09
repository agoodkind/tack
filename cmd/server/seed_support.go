package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// userIDNamespace derives seed-user UUIDs from email. Development
// authentication uses the raw user UUID as the bearer, and the same email
// produces the same development bearer after reseeding SQL data. This
// namespace separates user-email inputs from the workspaceNamespace and
// systemPropNS inputs for node IDs.
var userIDNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000003")

// userIDForEmail derives a deterministic seed-user UUID from the lowercased
// email. Case differences in reseed inputs do not change the derived UUID.
func userIDForEmail(email string) uuid.UUID {
	return uuid.NewSHA1(userIDNamespace, []byte(strings.ToLower(email)))
}

// ensureNode returns an existing node ID from the organization-scoped
// node_by_property index for typeKey and slug without further writes, or
// creates the node. lookupOrgID selects the organization for the lookup and
// matches parentID for a workspace. An organization node can use [uuid.Nil]
// as parentID and uses its own ID as orgID. Its lookupOrgID comes from an
// existing user membership or [uuid.Nil] when the user does not have one.
// CreateAtomic records the node change and, when parentID is not [uuid.Nil],
// the parent relationship change in the create transaction.
func ensureNode(ctx context.Context, s *fdbadapter.Stores, typeKey, slug, name string, parentID, lookupOrgID uuid.UUID) (uuid.UUID, error) {
	existing, err := findSeedNode(ctx, s, typeKey, slug, lookupOrgID)
	if err != nil || existing != uuid.Nil {
		return existing, err
	}
	id := seedNodeID(typeKey, slug, parentID)
	if err := createSeedNode(ctx, s, id, typeKey, slug, name, parentID); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// findSeedNode returns the ID of the node of typeKey with slug that the
// node_by_property index of lookupOrgID lists. It returns [uuid.Nil] when
// lookupOrgID is [uuid.Nil] or the index does not list a node.
func findSeedNode(ctx context.Context, s *fdbadapter.Stores, typeKey, slug string, lookupOrgID uuid.UUID) (uuid.UUID, error) {
	if lookupOrgID == uuid.Nil {
		return uuid.Nil, nil
	}
	slugRaw, err := encodeSeedSlug(ctx, slug)
	if err != nil {
		return uuid.Nil, err
	}
	existing, listErr := s.Nodes.ListByProperty(ctx, lookupOrgID, typeKey, "slug", slugRaw)
	if listErr == nil && len(existing) > 0 {
		telemetry.L(ctx).InfoContext(ctx, "seed.node_exists", "type", typeKey, "slug", slug, "id", existing[0].ID)
		return existing[0].ID, nil
	}
	return uuid.Nil, nil
}

// seedNodeID returns a fresh UUIDv7 for a root organization node, even when
// another organization uses the same slug. A workspace ID derives from its
// organization ID and slug and remains the same only while both inputs stay
// the same. Other child nodes receive fresh UUIDv7 IDs.
func seedNodeID(typeKey, slug string, parentID uuid.UUID) uuid.UUID {
	switch {
	case parentID == uuid.Nil:
		return node.NewOrgID()
	case typeKey == "workspace":
		return node.WorkspaceID(parentID, slug)
	default:
		return uuid.Must(uuid.NewV7())
	}
}

func encodeSeedSlug(ctx context.Context, slug string) (json.RawMessage, error) {
	slugRaw, err := json.Marshal(slug)
	if err != nil {
		slog.ErrorContext(ctx, "seed.encode_slug_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("seed: encode slug %q: %w", slug, err)
	}
	return slugRaw, nil
}

// createSeedNode writes these records in one CreateAtomic transaction:
//
//   - It writes the node with its ID, typeKey, slug, and name.
//   - It writes the materialized view.
//   - It writes the slug index entry.
//   - It writes the child_of relationship when parentID is not [uuid.Nil].
func createSeedNode(ctx context.Context, s *fdbadapter.Stores, id uuid.UUID, typeKey, slug, name string, parentID uuid.UUID) error {
	slugRaw, err := encodeSeedSlug(ctx, slug)
	if err != nil {
		return err
	}
	now := clock.Now().UTC()
	props := make(map[string]json.RawMessage)
	props["slug"] = slugRaw
	if parentID != uuid.Nil {
		parentRaw, marshalErr := json.Marshal(parentID.String())
		if marshalErr != nil {
			slog.ErrorContext(ctx, "seed.encode_parent_failed", slog.String("err", marshalErr.Error()))
			return fmt.Errorf("seed: encode parent_id: %w", marshalErr)
		}
		props["parent_id"] = parentRaw
	}

	// A node without a parent uses its own ID as orgID. Otherwise, this seed
	// helper uses parentID as orgID. The branch checks parent presence rather
	// than typeKey.
	orgID := parentID
	if parentID == uuid.Nil {
		orgID = id
	}

	n := &node.Node{
		ID: id, OrgID: orgID, NodeType: typeKey, Name: name,
		Props: props, CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: now, UpdatedAt: now,
	}
	view := &node.NodeView{
		ID: id, OrgID: orgID, NodeType: typeKey, Name: name,
		Props: props, CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: now, UpdatedAt: now,
	}
	var rels []*node.Relationship
	if parentID != uuid.Nil {
		rels = append(rels, &node.Relationship{
			OrgID: orgID, SourceID: id, RelationType: node.RelChildOf,
			TargetID: parentID, CreatedBy: uuid.Nil, Props: nil, CreatedAt: now,
		})
	}

	// CreateAtomic indexes slug in node_by_property for later ListByProperty
	// lookups.
	if err := s.Nodes.CreateAtomic(ctx, n, view, rels, []string{"slug"}, nil, nil); err != nil {
		slog.ErrorContext(ctx, "seed.create_node_failed", slog.String("type", typeKey), slog.String("err", err.Error()))
		return fmt.Errorf("seed: create node of type %s: %w", typeKey, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "seed.node_created", "type", typeKey, "slug", slug, "id", id)
	return nil
}

// generateToken returns a fresh random bearer token from the shared
// auth.NewBearerToken generator.
func generateToken() (string, error) {
	raw, err := auth.NewBearerToken()
	if err != nil {
		slog.Error("seed.token_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("seed: generate token: %w", err)
	}
	return raw, nil
}

// orgsExist queries org_members through a PostgreSQL pool and returns
// whether it contains a row. The seed guard checks SQL authentication
// memberships rather than FoundationDB product records. It returns false
// when the table does not exist before migration, which permits seed to
// apply migrations before creating initial data.
func orgsExist(ctx context.Context, cfg *config.Config) (bool, error) {
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, nil)
	if err != nil {
		slog.ErrorContext(ctx, "seed.orgs_exist_open_failed", slog.String("err", err.Error()))
		return false, fmt.Errorf("orgsExist: open db: %w", err)
	}
	defer pool.Close()

	var exists bool
	err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM org_members LIMIT 1)`).Scan(&exists)
	if err != nil {
		// A missing table before migration does not count as an existing
		// membership.
		if strings.Contains(err.Error(), "does not exist") {
			return false, nil
		}
		slog.ErrorContext(ctx, "seed.orgs_exist_query_failed", slog.String("err", err.Error()))
		return false, fmt.Errorf("orgsExist: query: %w", err)
	}
	return exists, nil
}
