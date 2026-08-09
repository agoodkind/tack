package datagen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
)

// PlanIdentities returns deterministic request identities without writing them.
func PlanIdentities(cfg *config.Config, seed int64, scale Scale) Identities {
	return plannedIdentities(cfg, seed, scale)
}

// BootstrapIdentities creates the SQL identities and FDB entry points.
func BootstrapIdentities(
	ctx context.Context,
	cfg *config.Config,
	seed int64,
	scale Scale,
) (Identities, error) {
	identities := plannedIdentities(cfg, seed, scale)
	if err := mintTokens(&identities, cfg); err != nil {
		return Identities{}, err
	}
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, nil)
	if err != nil {
		return Identities{}, loggedError(ctx, "qa datagen: open postgres", err)
	}
	defer pool.Close()
	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, pool)
	if err != nil {
		return Identities{}, loggedError(ctx, "qa datagen: open foundationdb", err)
	}
	users := postgres.NewUserRepo(pool)
	tokens := postgres.NewTokenRepo(pool)
	members := postgres.NewOrgMemberRepo(pool)
	seededOrgs := make(map[string]struct{})
	for workspaceIndex := range identities.Workspaces {
		workspace := &identities.Workspaces[workspaceIndex]
		if _, ok := seededOrgs[workspace.OrgID.String()]; !ok {
			if err := bootstrapOrg(ctx, stores, workspace); err != nil {
				return Identities{}, err
			}
			if err := service.NewSeeder(stores.PropertyDefs, stores.NodeTypes).SeedOrg(ctx, workspace.OrgID); err != nil {
				return Identities{}, loggedError(ctx, "qa datagen: seed org", err)
			}
			if err := seedQAPropertyDefs(ctx, stores.PropertyDefs, workspace.OrgID); err != nil {
				return Identities{}, err
			}
			if err := bootstrapActors(ctx, users, tokens, members, workspace); err != nil {
				return Identities{}, err
			}
			seededOrgs[workspace.OrgID.String()] = struct{}{}
		}
		if err := bootstrapWorkspace(ctx, stores, workspace); err != nil {
			return Identities{}, err
		}
	}
	if err := expireToken(ctx, pool, identities.ExpiredToken, identities.Workspaces[0].Actors[0]); err != nil {
		return Identities{}, err
	}
	return identities, nil
}

func mintTokens(identities *Identities, cfg *config.Config) error {
	rawTokens := make(map[uuid.UUID]string)
	for workspaceIndex := range identities.Workspaces {
		workspace := &identities.Workspaces[workspaceIndex]
		for actorIndex := range workspace.Actors {
			actor := &workspace.Actors[actorIndex]
			rawToken, ok := rawTokens[actor.UserID]
			if !ok {
				var err error
				rawToken, err = tokenFor()
				if err != nil {
					slog.Error("qa.datagen.token_generation_failed", slog.String("err", err.Error()))
					return fmt.Errorf("qa datagen: generate token: %w", err)
				}
				rawTokens[actor.UserID] = rawToken
			}
			actor.RawToken = rawToken
			if cfg.Env != "development" {
				actor.Token = rawToken
			}
		}
	}
	var err error
	identities.ExpiredToken, err = tokenFor()
	if err != nil {
		slog.Error("qa.datagen.expired_token_generation_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen: generate expired token: %w", err)
	}
	identities.BogusToken, err = tokenFor()
	if err != nil {
		slog.Error("qa.datagen.bogus_token_generation_failed", slog.String("err", err.Error()))
		return fmt.Errorf("qa datagen: generate bogus token: %w", err)
	}
	return nil
}

func bootstrapActors(
	ctx context.Context,
	users *postgres.UserRepo,
	tokens *postgres.TokenRepo,
	members *postgres.OrgMemberRepo,
	workspace *WorkspaceIdentity,
) error {
	for actorIndex := range workspace.Actors {
		actor := &workspace.Actors[actorIndex]
		stored, err := users.GetByEmail(ctx, actor.Email)
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return loggedError(ctx, "qa datagen: get user "+actor.Email, err)
		}
		if stored == nil {
			stored, err = users.Create(ctx, &user.User{
				ID: actor.UserID, Email: actor.Email, DisplayName: actor.DisplayName,
				AvatarURL: nil,
			})
			if err != nil {
				return loggedError(ctx, "qa datagen: create user "+actor.Email, err)
			}
		}
		actor.UserID = stored.ID
		if err := members.AddMember(ctx, &org.Member{
			ID: uuid.Nil, OrgID: workspace.OrgID, UserID: stored.ID,
			Role: 20,
		}); err != nil {
			return loggedError(ctx, "qa datagen: add org member "+actor.Email, err)
		}
		if _, err := tokens.Create(ctx, stored.ID, actor.RawToken, "qa-datagen"); err != nil &&
			!errors.Is(err, domain.ErrAlreadyExists) {
			return loggedError(ctx, "qa datagen: create token for "+actor.Email, err)
		}
	}
	return nil
}

func expireToken(ctx context.Context, pool *pgxpool.Pool, raw string, actor Actor) error {
	tokenRepo := postgres.NewTokenRepo(pool)
	if _, err := tokenRepo.Create(ctx, actor.UserID, raw, "qa-datagen-expired"); err != nil &&
		!errors.Is(err, domain.ErrAlreadyExists) {
		return loggedError(ctx, "qa datagen: create expired token", err)
	}
	sum := sha256.Sum256([]byte(raw))
	tag, err := pool.Exec(
		ctx,
		`UPDATE api_tokens SET expires_at = now() - interval '1 hour' WHERE token_hash = $1`,
		hex.EncodeToString(sum[:]),
	)
	if err != nil {
		return loggedError(ctx, "qa datagen: expire token", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("qa datagen: expire token updated %d rows", tag.RowsAffected())
	}
	return nil
}
