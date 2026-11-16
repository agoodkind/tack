package servercli

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// userIDNamespace derives stable user UUIDs from email for development bearer
// authentication across database resets. Separate node namespaces prevent an
// email and a node slug from deriving the same UUID.
var userIDNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000003")

// userIDForEmail derives a deterministic UUID from a lowercased email.
// Email case differences do not change the seed user's identity.
func userIDForEmail(email string) uuid.UUID {
	return uuid.NewSHA1(userIDNamespace, []byte(strings.ToLower(email)))
}

// ensureNode creates or re-verifies a generic node. It finds an existing
// node with (typeKey, slug) through the org-scoped node_by_property index
// and returns its ID without further writes. parentID may be [uuid.Nil] for
// org-level nodes. ensureNode uses lookupOrgID to query the property index
// for an existing node. For non-org nodes, lookupOrgID is parentID. For org
// nodes, orgID equals the node's own ID, and the caller supplies lookupOrgID
// from the user's existing org membership or [uuid.Nil] when a prior
// membership does not exist. In the create transaction, CreateAtomic writes
// a change event for the new node and writes one for its parent relationship
// when parentID is not [uuid.Nil].
func ensureNode(
	ctx context.Context,
	stores *fdbadapter.Stores,
	typeKey string,
	slug string,
	name string,
	parentID uuid.UUID,
	lookupOrgID uuid.UUID,
) (uuid.UUID, error) {
	existing, err := findSeedNode(ctx, stores, typeKey, slug, lookupOrgID)
	if err != nil || existing != uuid.Nil {
		return existing, err
	}

	id := seedNodeID(typeKey, slug, parentID)
	if err := createSeedNode(ctx, stores, id, typeKey, slug, name, parentID); err != nil {
		return uuid.Nil, err
	}

	return id, nil
}

// findSeedNode returns the ID of the node of typeKey with slug that the
// node_by_property index of lookupOrgID lists. It returns [uuid.Nil] when
// lookupOrgID is [uuid.Nil] or the index does not list a node.
func findSeedNode(
	ctx context.Context,
	stores *fdbadapter.Stores,
	typeKey string,
	slug string,
	lookupOrgID uuid.UUID,
) (uuid.UUID, error) {
	if lookupOrgID == uuid.Nil {
		return uuid.Nil, nil
	}

	slugRaw, err := encodeSeedSlug(ctx, slug)
	if err != nil {
		return uuid.Nil, err
	}

	existing, listErr := stores.Nodes.ListByProperty(ctx, lookupOrgID, typeKey, "slug", slugRaw)
	if listErr == nil && len(existing) > 0 {
		telemetry.L(ctx).InfoContext(
			ctx,
			"seed.node_exists",
			"type",
			typeKey,
			"slug",
			slug,
			"id",
			existing[0].ID,
		)

		return existing[0].ID, nil
	}

	return uuid.Nil, nil
}

// seedNodeID assigns random UUIDv7 identities to orgs and deeper nodes
// (TACK-230). Repeating an org slug does not repeat its identity. Workspaces
// derive stable identities from (orgID, slug) across database resets.
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
func createSeedNode(
	ctx context.Context,
	stores *fdbadapter.Stores,
	nodeID uuid.UUID,
	typeKey string,
	slug string,
	name string,
	parentID uuid.UUID,
) error {
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
			slog.ErrorContext(
				ctx,
				"seed.encode_parent_failed",
				slog.String("err", marshalErr.Error()),
			)

			return fmt.Errorf("seed: encode parent_id: %w", marshalErr)
		}

		props["parent_id"] = parentRaw
	}

	// A root node uses its own ID as OrgID. Other nodes use the parent ID
	// supplied for this seed hierarchy, independently of the node type name.
	orgID := parentID
	if parentID == uuid.Nil {
		orgID = nodeID
	}

	record := &node.Node{
		ID:        nodeID,
		OrgID:     orgID,
		NodeType:  typeKey,
		Name:      name,
		Props:     props,
		CreatedBy: uuid.Nil,
		UpdatedBy: uuid.Nil,
		CreatedAt: now,
		UpdatedAt: now,
	}
	view := &node.NodeView{
		ID:        nodeID,
		OrgID:     orgID,
		NodeType:  typeKey,
		Name:      name,
		Props:     props,
		CreatedBy: uuid.Nil,
		UpdatedBy: uuid.Nil,
		CreatedAt: now,
		UpdatedAt: now,
	}
	rels := seedParentRelationships(orgID, nodeID, parentID, now)

	// Index slug for ListByProperty lookups through node_by_property.
	if err := stores.Nodes.CreateAtomic(
		ctx,
		record,
		view,
		rels,
		[]string{"slug"},
		nil,
		nil,
	); err != nil {
		slog.ErrorContext(
			ctx,
			"seed.create_node_failed",
			slog.String("type", typeKey),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("seed: create node of type %s: %w", typeKey, err)
	}

	telemetry.L(ctx).InfoContext(
		ctx,
		"seed.node_created",
		"type",
		typeKey,
		"slug",
		slug,
		"id",
		nodeID,
	)

	return nil
}

// generateToken returns a fresh random bearer token in the format every
// issuer uses.
func generateToken() (string, error) {
	raw, err := auth.NewBearerToken()
	if err != nil {
		slog.Error("seed.token_failed", slog.String("err", err.Error()))

		return "", fmt.Errorf("seed: generate token: %w", err)
	}

	return raw, nil
}

// orgsExist checks org_members for seed-created membership records. SQL stores
// authentication and membership data; FDB stores product nodes. A missing
// pre-migration table returns false to permit seed initialization.
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
		// Treat an absent pre-migration table as empty.
		if strings.Contains(err.Error(), "does not exist") {
			return false, nil
		}

		slog.ErrorContext(ctx, "seed.orgs_exist_query_failed", slog.String("err", err.Error()))

		return false, fmt.Errorf("orgsExist: query: %w", err)
	}

	return exists, nil
}

func seedParentRelationships(
	orgID uuid.UUID,
	nodeID uuid.UUID,
	parentID uuid.UUID,
	now time.Time,
) []*node.Relationship {
	var rels []*node.Relationship
	if parentID != uuid.Nil {
		rels = append(
			rels,
			&node.Relationship{
				OrgID:        orgID,
				SourceID:     nodeID,
				RelationType: node.RelChildOf,
				TargetID:     parentID,
				CreatedBy:    uuid.Nil,
				Props:        nil,
				CreatedAt:    now,
			},
		)
	}

	return rels
}
