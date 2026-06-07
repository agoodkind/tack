package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/migrations"
)

type seedInput struct {
	clispec.InputMarker
	AllowReseed bool
}

// seedOp seeds the initial user, org, workspace, and API token. It refuses to
// run against a non-empty database unless --allow-reseed is passed.
func seedOp(f *cli.Factory) clispec.Operation[seedInput] {
	return clispec.Operation[seedInput]{
		Name:  clispec.Name{Canonical: "seed"},
		Short: "Seed the initial user, org, workspace, and API token",
		Params: []clispec.Param[seedInput]{
			clispec.BoolParam("allow-reseed",
				"Allow seed against a non-empty database; required for re-seeding production",
				false, func(in *seedInput, v bool) { in.AllowReseed = v }),
		},
		New: func() seedInput { return seedInput{} },
		Run: func(ctx context.Context, in seedInput, sink clispec.ResultSink) error {
			nonEmpty, err := orgsExist(ctx, f.Cfg)
			if err != nil {
				return err
			}
			if nonEmpty && !in.AllowReseed {
				return fmt.Errorf("seed refused: database already contains at least one org; pass --allow-reseed to override (TACK-230, 2026-05-09 parallel-org outage)")
			}
			if f.Cfg.SeedEmail == "" || f.Cfg.SeedName == "" {
				return fmt.Errorf("seed: SEED_EMAIL and SEED_NAME are both required")
			}
			execSeed(ctx, f.Cfg)
			return sink.Text(ctx, "seed complete")
		},
	}
}

// execSeed creates the initial user, org, and workspace using the generic Node
// primitives. This is the one place in the system that references specific
// NodeType names (via service.Seeder constants).
func execSeed(ctx context.Context, cfg *config.Config) {
	// Seed runs without audit emission: it produces hundreds of node.create
	// events with a system-shaped actor that aren't user actions. The
	// suppression marker propagates through the same Recorder the running
	// server would use, so the wiring is already correct.
	ctx = audit.WithSuppressed(ctx)
	ctx, span := telemetry.StartSpan(ctx, "seed.run", trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("command", "seed"))
	log := telemetry.L(ctx)

	if err := postgres.Migrate(ctx, cfg.DatabaseURL, migrations.FS); err != nil {
		log.Error("seed: migrate", "err", err)
		os.Exit(1)
	}

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		log.Error("seed: postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile, pool)
	if err != nil {
		log.Error("seed: foundationdb", "err", err)
		os.Exit(1)
	}

	userRepo := postgres.NewUserRepo(pool)
	tokenRepo := postgres.NewTokenRepo(pool)
	members := postgres.NewOrgMemberRepo(pool)
	seeder := service.NewSeeder(fdbStores.PropertyDefs, fdbStores.NodeTypes)

	u, err := userRepo.GetByEmail(ctx, cfg.SeedEmail)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		log.Error("seed: get user", "err", err)
		os.Exit(1)
	}
	if u == nil {
		u, err = userRepo.Create(ctx, &user.User{
			ID:          userIDForEmail(cfg.SeedEmail),
			Email:       cfg.SeedEmail,
			DisplayName: cfg.SeedName,
		})
		if err != nil {
			log.Error("seed: create user", "err", err)
			os.Exit(1)
		}
		log.Info("seed: created user", "id", u.ID, "email", u.Email)
	} else {
		log.Info("seed: user exists", "id", u.ID, "email", u.Email)
	}

	// The org case has no parent, so orgID == the node's own ID. To detect an
	// existing org by slug via the org-scoped node_by_property index, we need a
	// known orgID, derived from the user's existing org membership (if any). On
	// a first seed the membership list is empty and a new org is created.
	existingOrgIDs, memberErr := members.ListOrgIDsForUser(ctx, u.ID)
	if memberErr != nil {
		log.WarnContext(ctx, "seed: list org memberships", "err", memberErr)
	}
	var knownOrgID uuid.UUID
	if len(existingOrgIDs) > 0 {
		knownOrgID = existingOrgIDs[0]
	}
	orgID := ensureNode(ctx, fdbStores, "org", cfg.SeedOrgSlug, cfg.SeedOrgName, uuid.Nil, knownOrgID)
	seeder.SeedOrg(ctx, orgID)

	if err := members.AddMember(ctx, &org.Member{OrgID: orgID, UserID: u.ID, Role: 20}); err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		log.WarnContext(ctx, "seed: add org member", "err", err)
	}

	// orgID is the parent of the workspace, so it doubles as the lookup orgID.
	wsID := ensureNode(ctx, fdbStores, "workspace", cfg.SeedWorkspaceSlug, cfg.SeedWorkspaceName, orgID, orgID)

	// Production mode: SHA-256 hash of the bearer is looked up in api_tokens.
	// Dev mode (ENV=development): the bearer is the raw user UUID directly, no
	// api_tokens lookup. Print both so the operator picks the right one.
	raw := cfg.SeedAPIToken
	if raw == "" {
		raw = generateToken()
	}
	if _, err := tokenRepo.Create(ctx, u.ID, raw, "seed"); err != nil {
		log.Info("seed: prod token already exists, skipping")
	} else {
		_, _ = fmt.Fprintf(os.Stdout, "\nProduction-mode API token (copy now; not shown again):\n  %s\n", raw)
	}
	_, _ = fmt.Fprintf(os.Stdout, "\nDev-mode bearer (stable across wipes, derived from %s):\n  %s\n\n", cfg.SeedEmail, u.ID)
	_, _ = fmt.Fprintln(os.Stdout, "Add to your MCP config:")
	_, _ = fmt.Fprintln(os.Stdout, "  \"Authorization\": \"Bearer <token-above>\"")

	log.Info("seed complete", "user_id", u.ID, "org_id", orgID, "workspace_id", wsID)
}
