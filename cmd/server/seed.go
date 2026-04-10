package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/domain/workspace"
	"goodkind.io/tack/migrations"
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

	userRepo := postgres.NewUserRepo(pool)
	orgRepo := postgres.NewOrgRepo(pool)
	wsRepo := postgres.NewWorkspaceRepo(pool)
	tokenRepo := postgres.NewTokenRepo(pool)

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
	o, err := orgRepo.GetBySlug(ctx, cfg.SeedOrgSlug)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		slog.Error("seed: get org", "err", err)
		os.Exit(1)
	}
	if o == nil {
		o, err = orgRepo.Create(ctx, &org.Org{
			Name: cfg.SeedOrgName,
			Slug: cfg.SeedOrgSlug,
		})
		if err != nil {
			slog.Error("seed: create org", "err", err)
			os.Exit(1)
		}
		if err := orgRepo.AddMember(ctx, &org.Member{
			OrgID:  o.ID,
			UserID: u.ID,
			Role:   20, // admin
		}); err != nil {
			slog.Error("seed: add org member", "err", err)
			os.Exit(1)
		}
		slog.Info("seed: created org", "id", o.ID, "slug", o.Slug)
	} else {
		slog.Info("seed: org exists", "id", o.ID, "slug", o.Slug)
	}

	// ── Workspace ─────────────────────────────────────────────────────────────
	ws, err := wsRepo.GetBySlug(ctx, cfg.SeedWorkspaceSlug)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		slog.Error("seed: get workspace", "err", err)
		os.Exit(1)
	}
	if ws == nil {
		ws, err = wsRepo.Create(ctx, &workspace.Workspace{
			OrgID: o.ID,
			Name:  cfg.SeedWorkspaceName,
			Slug:  cfg.SeedWorkspaceSlug,
		})
		if err != nil {
			slog.Error("seed: create workspace", "err", err)
			os.Exit(1)
		}
		slog.Info("seed: created workspace", "id", ws.ID, "slug", ws.Slug)
	} else {
		slog.Info("seed: workspace exists", "id", ws.ID, "slug", ws.Slug)
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
		"org_id", o.ID,
		"workspace_id", ws.ID,
	)
}

func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("seed: generate token: " + err.Error())
	}
	return "tack_" + base64.RawURLEncoding.EncodeToString(b)
}
