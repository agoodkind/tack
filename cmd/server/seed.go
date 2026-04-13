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
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/migrations"
	"goodkind.io/tack/internal/service"
	"github.com/google/uuid"
)

func runSeed(cfg *config.Config) {
	if cfg.SeedEmail == "" || cfg.SeedName == "" {
		slog.Error("seed requires SEED_EMAIL and SEED_NAME")
		os.Exit(1)
	}

	ctx := context.Background()

	// Migrate first — safe to call on an already-migrated DB.
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

	// FDB stores for entity data (org, workspace).
	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile, pool)
	if err != nil {
		slog.Error("seed: foundationdb", "err", err)
		os.Exit(1)
	}

	userRepo  := postgres.NewUserRepo(pool)
	tokenRepo := postgres.NewTokenRepo(pool)
	members   := postgres.NewOrgMemberRepo(pool)
	seeder    := service.NewWorkspaceSeeder(fdbStores.Properties, fdbStores.NodeTypes)

	// ── User ─────────────────────────────────────────────────────────────────
	u, err := userRepo.GetByEmail(ctx, cfg.SeedEmail)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		slog.Error("seed: get user", "err", err)
		os.Exit(1)
	}
	if u == nil {
		u, err = userRepo.Create(ctx, &user.User{
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

	// ── Org ──────────────────────────────────────────────────────────────────
	orgID := uuid.New()
	now := time.Now()

	// Check if org already exists by slug
	existingOrgID, err := fdbStores.Entities.GetBySlug(ctx, "org", cfg.SeedOrgSlug)
	if err == nil {
		// Org exists — still re-seed types to propagate feature/hierarchy changes.
		orgID = existingOrgID
		seeder.SeedOrg(ctx, orgID)
		slog.Info("seed: org exists", "id", orgID, "slug", cfg.SeedOrgSlug)
	} else if errors.Is(err, domain.ErrNotFound) {
		// Create new org
		slugBytes, _ := json.Marshal(cfg.SeedOrgSlug)
		orgProps := make(map[string]json.RawMessage)
		orgProps["slug"] = slugBytes

		orgNV := &node.NodeValue{
			ID:        orgID,
			OrgID:     orgID,
			NodeType:  "org",
			Name:      cfg.SeedOrgName,
			CreatedAt: now,
			UpdatedAt: now,
		}

		orgView := &node.NodeListView{
			Version:     node.ViewVersion1,
			ID:          orgID,
			OrgID:       orgID,
			NodeType:    "org",
			Name:        cfg.SeedOrgName,
			CreatedAt:   now,
			UpdatedAt:   now,
			CustomProps: orgProps,
		}

		if _, err := fdbStores.Entities.CreateAtomic(ctx, orgID, orgID, orgNV, nil, orgView, nil, nil, uuid.Nil); err != nil {
			slog.Error("seed: create org", "err", err)
			os.Exit(1)
		}

		// Write slug index
		if err := fdbStores.Entities.WriteSlugIndex(ctx, "org", cfg.SeedOrgSlug, orgID); err != nil {
			slog.Error("seed: write org slug index", "err", err)
			os.Exit(1)
		}

		// Add user as org member
		if err := members.AddMember(ctx, &org.Member{
			OrgID:  orgID,
			UserID: u.ID,
			Role:   20, // admin
		}); err != nil {
			slog.Error("seed: add org member", "err", err)
			os.Exit(1)
		}

		// Seed org-level property defs
		seeder.SeedOrg(ctx, orgID)

		slog.Info("seed: created org", "id", orgID, "slug", cfg.SeedOrgSlug)
	} else {
		slog.Error("seed: lookup org", "err", err)
		os.Exit(1)
	}

	// ── Workspace ─────────────────────────────────────────────────────────────
	wsID := uuid.New()
	wsResolve, err := fdbStores.Entities.GetBySlug(ctx, "workspace", cfg.SeedWorkspaceSlug)
	if err == nil {
		// Workspace exists — still re-seed types/props to propagate feature changes.
		wsID = wsResolve
		seeder.SeedWorkspace(ctx, orgID, wsID)
		slog.Info("seed: workspace exists", "id", wsID, "slug", cfg.SeedWorkspaceSlug)
	} else if errors.Is(err, domain.ErrNotFound) {
		// Create new workspace
		slugBytes, _ := json.Marshal(cfg.SeedWorkspaceSlug)
		wsProps := make(map[string]json.RawMessage)
		wsProps["slug"] = slugBytes

		wsNV := &node.NodeValue{
			ID:          wsID,
			OrgID:       orgID,
			WorkspaceID: uuid.Nil,
			NodeType:    "workspace",
			Name:        cfg.SeedWorkspaceName,
			CreatedAt:   now,
			UpdatedAt:   now,
		}

		wsView := &node.NodeListView{
			Version:     node.ViewVersion1,
			ID:          wsID,
			OrgID:       orgID,
			WorkspaceID: uuid.Nil,
			NodeType:    "workspace",
			Name:        cfg.SeedWorkspaceName,
			CreatedAt:   now,
			UpdatedAt:   now,
			CustomProps: wsProps,
		}

		if _, err := fdbStores.Entities.CreateAtomic(ctx, orgID, wsID, wsNV, nil, wsView, nil, nil, uuid.Nil); err != nil {
			slog.Error("seed: create workspace", "err", err)
			os.Exit(1)
		}

		// Write slug index
		if err := fdbStores.Entities.WriteSlugIndex(ctx, "workspace", cfg.SeedWorkspaceSlug, wsID); err != nil {
			slog.Error("seed: write workspace slug index", "err", err)
			os.Exit(1)
		}

		// Seed workspace-level property defs
		seeder.SeedWorkspace(ctx, orgID, wsID)

		slog.Info("seed: created workspace", "id", wsID, "slug", cfg.SeedWorkspaceSlug)
	} else {
		slog.Error("seed: lookup workspace", "err", err)
		os.Exit(1)
	}

	// ── API token ─────────────────────────────────────────────────────────────
	raw := cfg.SeedAPIToken
	if raw == "" {
		raw = generateToken()
	}
	if _, err := tokenRepo.Create(ctx, u.ID, raw, "seed"); err != nil {
		// Duplicate hash means this exact token was already seeded — not an error.
		slog.Info("seed: token already exists or conflict, skipping")
	} else {
		fmt.Printf("\n✓ API token (copy now — not shown again):\n\n  %s\n\n", raw)
		fmt.Printf("Add to your MCP config:\n")
		fmt.Printf(`  "Authorization": "Bearer %s"`+"\n\n", raw)
	}

	slog.Info("seed complete",
		"user_id", u.ID,
		"org_id", orgID,
		"workspace_id", wsID,
	)
}

func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("seed: generate token: " + err.Error())
	}
	return "tack_" + base64.RawURLEncoding.EncodeToString(b)
}
