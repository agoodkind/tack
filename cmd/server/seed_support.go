package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
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

// ensureNode creates (or re-verifies) a generic node. When a node with
// (typeKey, slug) already exists it is detected via the org-scoped
// node_by_property index and its ID is returned with no further writes.
// parentID may be uuid.Nil for org-level nodes. lookupOrgID is the orgID used
// to query the property index for an existing node: for non-org nodes this is
// the parentID; for org nodes (where orgID == the node's own ID) the caller
// supplies the orgID from the user's existing org membership, or uuid.Nil when
// no prior membership exists.
func ensureNode(ctx context.Context, s *fdbadapter.Stores, typeKey, slug, name string, parentID, lookupOrgID uuid.UUID) (uuid.UUID, bool, error) {
	log := telemetry.L(ctx)
	slugRaw, err := json.Marshal(slug)
	if err != nil {
		slog.ErrorContext(ctx, "seed.encode_slug_failed", slog.String("err", err.Error()))
		return uuid.Nil, false, fmt.Errorf("seed: encode slug %q: %w", slug, err)
	}
	if lookupOrgID != uuid.Nil {
		existing, listErr := s.Nodes.ListByProperty(ctx, lookupOrgID, typeKey, "slug", slugRaw)
		if listErr == nil && len(existing) > 0 {
			log.InfoContext(ctx, "seed.node_exists", "type", typeKey, "slug", slug, "id", existing[0].ID)
			return existing[0].ID, false, nil
		}
	}

	now := clock.Now().UTC()

	// Org nodes get a fresh random UUIDv7 (TACK-230). Two calls with the same
	// slug produce different IDs, which is correct: org identity is row-level,
	// not derivable from a slug. A workspace under an org derives its ID from
	// (orgID, slug) and stays deterministic across wipe-and-reseed; anything
	// deeper uses V7.
	var id uuid.UUID
	switch {
	case parentID == uuid.Nil:
		id = node.NewOrgID()
	case typeKey == "workspace":
		id = node.WorkspaceID(parentID, slug)
	default:
		id = uuid.Must(uuid.NewV7())
	}

	props := make(map[string]json.RawMessage)
	props["slug"] = slugRaw
	if parentID != uuid.Nil {
		parentRaw, marshalErr := json.Marshal(parentID.String())
		if marshalErr != nil {
			slog.ErrorContext(ctx, "seed.encode_parent_failed", slog.String("err", marshalErr.Error()))
			return uuid.Nil, false, fmt.Errorf("seed: encode parent_id: %w", marshalErr)
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
		return uuid.Nil, false, fmt.Errorf("seed: create node %s: %w", typeKey, err)
	}
	log.InfoContext(ctx, "seed.node_created", "type", typeKey, "slug", slug, "id", id)
	return id, true, nil
}

// generateToken returns a fresh random bearer token.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		slog.Error("seed.token_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("seed: generate token: %w", err)
	}
	return "tack_" + base64.RawURLEncoding.EncodeToString(b), nil
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
