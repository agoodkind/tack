package main

import (
	"context"
	"fmt"
	"os"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/telemetry"
)

func seedToken(
	ctx context.Context,
	cfg *config.Config,
	tokenRepo *postgres.TokenRepo,
	seededUser *user.User,
	orgID uuid.UUID,
	recorder audit.Recorder,
) error {
	log := telemetry.L(ctx)
	raw := cfg.SeedAPIToken
	if raw == "" {
		generatedToken, err := generateToken()
		if err != nil {
			return err
		}
		raw = generatedToken
	}
	seededToken, err := tokenRepo.Create(ctx, seededUser.ID, raw, "seed")
	if err != nil {
		log.InfoContext(ctx, "seed.prod_token_exists")
	} else {
		if err := recordSeedToken(ctx, recorder, seededToken, orgID); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(os.Stdout, "\nProduction-mode API token (copy now; not shown again):\n  %s\n", raw)
	}
	_, _ = fmt.Fprintf(os.Stdout, "\nDev-mode bearer (stable across wipes, derived from %s):\n  %s\n\n", cfg.SeedEmail, seededUser.ID)
	_, _ = fmt.Fprintln(os.Stdout, "Add to your MCP config:")
	_, _ = fmt.Fprintln(os.Stdout, "  \"Authorization\": \"Bearer <token-above>\"")
	return nil
}
