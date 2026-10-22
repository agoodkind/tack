package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/telemetry"
)

func ensureSeedUser(ctx context.Context, cfg *config.Config, userRepo *postgres.UserRepo) (*user.User, error) {
	log := telemetry.L(ctx)
	existing, err := userRepo.GetByEmail(ctx, cfg.SeedEmail)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		slog.ErrorContext(ctx, "seed.get_user_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("seed: get user: %w", err)
	}
	if existing != nil {
		log.InfoContext(ctx, "seed.user_exists", "id", existing.ID, "email", existing.Email)
		return existing, nil
	}
	operation, err := seedOperation(ctx, audit.VerbUserCreate, audit.SystemOrgID())
	if err != nil {
		return nil, err
	}
	created, err := userRepo.Create(ctx, operation, &user.User{
		ID:          userIDForEmail(cfg.SeedEmail),
		Email:       cfg.SeedEmail,
		DisplayName: cfg.SeedName,
		AvatarURL:   nil,
		CreatedAt:   time.Time{},
		UpdatedAt:   time.Time{},
	})
	if err != nil {
		slog.ErrorContext(ctx, "seed.create_user_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("seed: create user: %w", err)
	}
	log.InfoContext(ctx, "seed.user_created", "id", created.ID, "email", created.Email)
	return created, nil
}
