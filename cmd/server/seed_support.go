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

// userIDNamespace gives the seed user a deterministic UUID derived from email.
// The dev-mode auth path uses the raw user UUID as a Bearer token, so a stable
// UUID across Yugabyte wipes means a stable MCP config that doesn't rebreak
// every time we re-seed. The namespace is distinct from the node-side
// namespaces (workspaceNamespace, systemPropNS) on purpose so a slug and an
// email can never collide on the same UUID.
var userIDNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000003")

// userIDForEmail derives a deterministic UUID for a seed user from their email.
// Lowercased so case differences in re-seed inputs do not produce drift.
func userIDForEmail(email string) uuid.UUID {
	return uuid.NewSHA1(userIDNamespace, []byte(strings.ToLower(email)))
}

// ensureNode creates or re-verifies a generic node. It finds an existing node
// with (typeKey, slug) through the org-scoped node_by_property index and returns
// its ID without further writes. parentID may be [uuid.Nil] for org-level nodes.
// ensureNode uses lookupOrgID to query the property index for an existing node.
// For non-org nodes, lookupOrgID is parentID. For org nodes, orgID equals the
// node's own ID, and the caller supplies lookupOrgID from the user's existing
// org membership or [uuid.Nil] when a prior membership does not exist. In
// the create transaction, CreateAtomic writes a change event for the new node
// and writes one for its parent relationship when parentID is not [uuid.Nil].
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

// seedNodeID returns the ID of a new seed node. Org nodes get a fresh random
// UUIDv7 (TACK-230). Two calls with the same slug produce different IDs, which
// is correct: org identity is row-level, not derivable from a slug. A
// workspace under an org derives its ID from (orgID, slug) and stays
// deterministic across wipe-and-reseed; anything deeper uses V7.
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

// createSeedNode writes the node id of typeKey with slug and name under
// parentID, its view, its slug index entry, and its child_of edge in one
// CreateAtomic transaction.
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

	// A root node (no parent) is its own org: OrgID == ID. Every other node
	// inherits its org from the parent. Rooted by presence, not by type name.
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

	// Mark slug as indexed so ListByProperty works for it. The node_by_property
	// index written here replaces the former address_index write.
	if err := s.Nodes.CreateAtomic(ctx, n, view, rels, []string{"slug"}, nil, nil); err != nil {
		slog.ErrorContext(ctx, "seed.create_node_failed", slog.String("type", typeKey), slog.String("err", err.Error()))
		return fmt.Errorf("seed: create node of type %s: %w", typeKey, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "seed.node_created", "type", typeKey, "slug", slug, "id", id)
	return nil
}

// generateToken returns a fresh random bearer token in the one shape every
// issuer produces.
func generateToken() (string, error) {
	raw, err := auth.NewBearerToken()
	if err != nil {
		slog.Error("seed.token_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("seed: generate token: %w", err)
	}
	return raw, nil
}

// orgsExist opens a read-only Postgres connection and returns true if
// org_members contains at least one row, which implies at least one org has
// been seeded. It uses org_members rather than an entity table because SQL
// holds the only authoritative org registry (FDB holds product data, not
// auth). If the table does not yet exist (pre-migration), it returns false so
// seed can proceed to run migrations first.
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
		// Table does not exist yet (pre-migration); treat as empty.
		if strings.Contains(err.Error(), "does not exist") {
			return false, nil
		}
		slog.ErrorContext(ctx, "seed.orgs_exist_query_failed", slog.String("err", err.Error()))
		return false, fmt.Errorf("orgsExist: query: %w", err)
	}
	return exists, nil
}
