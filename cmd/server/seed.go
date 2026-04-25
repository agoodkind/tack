package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/migrations"
)

// userIDNamespace gives the seed user a deterministic UUID derived from email.
// The dev-mode auth path uses the raw user UUID as a Bearer token, so a stable
// UUID across Yugabyte wipes means a stable MCP config that doesn't rebreak
// every time we re-seed. The namespace is distinct from the node-side
// namespaces (orgNamespace, workspaceNamespace, systemPropNS) on purpose so
// a slug and an email can never collide on the same UUID.
var userIDNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000003")

// userIDForEmail derives a deterministic UUID for a seed user from their email.
// Lowercased so case differences in re-seed inputs do not produce drift.
func userIDForEmail(email string) uuid.UUID {
	return uuid.NewSHA1(userIDNamespace, []byte(strings.ToLower(email)))
}

// runSeed creates the initial user, org, and workspace using the generic Node
// primitives. This is the one place in the system that references specific
// NodeType names (via service.Seeder constants).
func runSeed(cfg *config.Config) {
	if cfg.SeedEmail == "" || cfg.SeedName == "" {
		slog.Error("seed requires SEED_EMAIL and SEED_NAME")
		os.Exit(1)
	}

	ctx := context.Background()

	if err := postgres.Migrate(ctx, cfg.DatabaseURL, migrations.FS); err != nil {
		slog.Error("seed: migrate", "err", err)
		os.Exit(1)
	}

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, nil)
	if err != nil {
		slog.Error("seed: postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile, pool)
	if err != nil {
		slog.Error("seed: foundationdb", "err", err)
		os.Exit(1)
	}

	userRepo := postgres.NewUserRepo(pool)
	tokenRepo := postgres.NewTokenRepo(pool)
	members := postgres.NewOrgMemberRepo(pool)
	seeder := service.NewSeeder(fdbStores.PropertyDefs, fdbStores.NodeTypes)

	// ── User ───────────────────────────────────────────────────────────────────
	u, err := userRepo.GetByEmail(ctx, cfg.SeedEmail)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		slog.Error("seed: get user", "err", err)
		os.Exit(1)
	}
	if u == nil {
		u, err = userRepo.Create(ctx, &user.User{
			ID:          userIDForEmail(cfg.SeedEmail),
			Email:       cfg.SeedEmail,
			DisplayName: cfg.SeedName,
		})
		if err != nil {
			slog.Error("seed: create user", "err", err)
			os.Exit(1)
		}
		slog.Info("seed: created user", "id", u.ID, "email", u.Email)
	} else {
		slog.Info("seed: user exists", "id", u.ID, "email", u.Email)
	}

	// ── Org ────────────────────────────────────────────────────────────────────
	orgID := ensureNode(ctx, fdbStores, "org", cfg.SeedOrgSlug, cfg.SeedOrgName, uuid.Nil)
	seeder.SeedOrg(ctx, orgID)

	// Make sure the user is an org admin.
	if err := members.AddMember(ctx, &org.Member{
		OrgID:  orgID,
		UserID: u.ID,
		Role:   20,
	}); err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		slog.Warn("seed: add org member", "err", err)
	}

	// ── Workspace ──────────────────────────────────────────────────────────────
	wsID := ensureNode(ctx, fdbStores, "workspace", cfg.SeedWorkspaceSlug, cfg.SeedWorkspaceName, orgID)

	// ── API token ──────────────────────────────────────────────────────────────
	// Production mode: SHA-256 hash of the bearer is looked up in api_tokens.
	// Dev mode (ENV=development): the bearer is the raw user UUID directly,
	// no api_tokens lookup. Print both so the operator picks the right one
	// for their environment.
	raw := cfg.SeedAPIToken
	if raw == "" {
		raw = generateToken()
	}
	if _, err := tokenRepo.Create(ctx, u.ID, raw, "seed"); err != nil {
		slog.Info("seed: prod token already exists, skipping")
	} else {
		fmt.Printf("\nProduction-mode API token (copy now; not shown again):\n  %s\n", raw)
	}
	fmt.Printf("\nDev-mode bearer (stable across wipes, derived from %s):\n  %s\n\n", cfg.SeedEmail, u.ID)
	fmt.Printf("Add to your MCP config:\n")
	fmt.Printf("  \"Authorization\": \"Bearer <token-above>\"\n\n")

	slog.Info("seed complete",
		"user_id", u.ID,
		"org_id", orgID,
		"workspace_id", wsID,
	)
}

// ensureNode creates (or re-verifies) a generic node with a slug index. When a
// node with (typeKey, slug) already exists, its ID is returned and no write
// happens beyond the slug index refresh. parentID may be uuid.Nil for org-level
// nodes.
func ensureNode(ctx context.Context, s *fdbadapter.Stores, typeKey, slug, name string, parentID uuid.UUID) uuid.UUID {
	if existing, err := s.Nodes.GetSlug(ctx, typeKey, slug); err == nil && existing != uuid.Nil {
		slog.Info("seed: node exists", "type", typeKey, "slug", slug, "id", existing)
		return existing
	}

	now := time.Now().UTC()

	// Deterministic ID derivation for the seed root nodes. A wipe and reseed
	// with the same slug produces byte-identical UUIDs, which keeps NodeType
	// and PropertyDef IDs stable across wipes.
	//
	// Root (no parent): the org node owns its slug. ID derives from the slug
	// alone. Workspace under org: ID derives from (orgID, slug).
	// Anything deeper still uses V7 because deeper seed nodes are not part of
	// the determinism contract today.
	var id uuid.UUID
	switch {
	case parentID == uuid.Nil:
		id = node.OrgID(slug)
	case typeKey == "workspace":
		id = node.WorkspaceID(parentID, slug)
	default:
		id = uuid.Must(uuid.NewV7())
	}

	props := make(map[string]json.RawMessage)
	slugRaw, _ := json.Marshal(slug)
	props["slug"] = slugRaw
	if parentID != uuid.Nil {
		parentRaw, _ := json.Marshal(parentID.String())
		props["parent_id"] = parentRaw
	}

	// A root node (no parent) is its own org: OrgID == ID. Every other node
	// inherits its org from the parent. Rooted by presence, not by type name.
	orgID := parentID
	if parentID == uuid.Nil {
		orgID = id
	}

	n := &node.Node{
		ID:        id,
		OrgID:     orgID,
		NodeType:  typeKey,
		Name:      name,
		Props:     props,
		CreatedAt: now,
		UpdatedAt: now,
	}
	view := &node.NodeView{
		ID:        id,
		OrgID:     orgID,
		NodeType:  typeKey,
		Name:      name,
		Props:     props,
		CreatedAt: now,
		UpdatedAt: now,
	}
	var rels []*node.Relationship
	if parentID != uuid.Nil {
		rels = append(rels, &node.Relationship{
			OrgID:        orgID,
			SourceID:     id,
			RelationType: node.RelChildOf,
			TargetID:     parentID,
			CreatedAt:    now,
		})
	}

	// Mark slug as indexed so ListByProperty works for it.
	if err := s.Nodes.CreateAtomic(ctx, n, view, rels, []string{"slug"}); err != nil {
		slog.Error("seed: create node", "type", typeKey, "err", err)
		os.Exit(1)
	}
	if err := s.Nodes.WriteSlug(ctx, typeKey, slug, id); err != nil {
		slog.Error("seed: write slug", "type", typeKey, "err", err)
		os.Exit(1)
	}
	slog.Info("seed: created node", "type", typeKey, "slug", slug, "id", id)
	return id
}

func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("seed: generate token: " + err.Error())
	}
	return "tack_" + base64.RawURLEncoding.EncodeToString(b)
}
