package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/adapters/postgres/migration"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/migrations"
)

type seedInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
	AllowReseed         bool
}

// seedOp seeds the initial user, org, workspace, and API token. It refuses to
// run against a non-empty database unless --allow-reseed is passed.
func seedOp(f *cli.Factory) clispec.Operation[seedInput] {
	return clispec.Operation[seedInput]{
		Name:     clispec.Name{Canonical: "seed"},
		Lifetime: clispec.Permanent,
		Audit: audit.Spec{
			Verb:    string(audit.VerbBootstrapSeed),
			Mutates: true,
		},
		Short: "Seed the initial user, org, workspace, and API token",
		Params: []clispec.Param[seedInput]{
			clispec.BoolParam(
				"allow-reseed",
				"Allow seed against a non-empty database; required for re-seeding production",
				false,
				func(in *seedInput, v bool) { in.AllowReseed = v },
			),
		},
		New: func() seedInput { return seedInput{AllowReseed: false} },
		Run: func(ctx context.Context, in seedInput, sink clispec.ResultSink) error {
			nonEmpty, err := orgsExist(ctx, f.Cfg)
			if err != nil {
				return err
			}

			if nonEmpty && !in.AllowReseed {
				return errors.New(
					"seed refused: database already contains at least one org; pass --allow-reseed to override (TACK-230, 2026-05-09 parallel-org outage)",
				)
			}

			if f.Cfg.SeedEmail == "" || f.Cfg.SeedName == "" {
				return errors.New("seed: SEED_EMAIL and SEED_NAME are both required")
			}

			if err := execSeed(ctx, f.Cfg); err != nil {
				return err
			}

			return sink.WriteText(ctx, "seed complete")
		},
	}
}

// enableSeedSearchWork turns on search work scheduling in the seed stores
// when OPENSEARCH_ENDPOINT is set.
func enableSeedSearchWork(cfg *config.Config, stores *fdbadapter.Stores) {
	if cfg.SearchEndpoint != "" {
		stores.EnableSearchWork()
	}
}

// execSeed creates the initial user, org, and workspace using the generic Node
// primitives. This is the one place in the system that references specific
// NodeType names (via service.Seeder constants). It returns any fatal error to
// the caller rather than exiting in place.
func execSeed(ctx context.Context, cfg *config.Config) error {
	ctx, span := telemetry.StartSpan(ctx, "seed.run", trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("command", "seed"))
	log := telemetry.L(ctx)

	if err := migration.Migrate(ctx, cfg.DatabaseURL, migrations.FS); err != nil {
		slog.ErrorContext(ctx, "seed.migrate_failed", slog.String("err", err.Error()))
		return fmt.Errorf("seed: migrate: %w", err)
	}

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		slog.ErrorContext(ctx, "seed.postgres_failed", slog.String("err", err.Error()))
		return fmt.Errorf("seed: postgres: %w", err)
	}
	defer pool.Close()

	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, pool)
	if err != nil {
		slog.ErrorContext(ctx, "seed.foundationdb_failed", slog.String("err", err.Error()))
		return fmt.Errorf("seed: foundationdb: %w", err)
	}

	enableSeedSearchWork(cfg, fdbStores)

	userRepo := postgres.NewUserRepo(pool)
	tokenRepo := postgres.NewTokenRepo(pool)
	members := postgres.NewOrgMemberRepo(pool)
	seeder := service.NewSeeder(fdbStores.PropertyDefs, fdbStores.NodeTypes)

	u, err := ensureSeedUser(ctx, cfg, userRepo)
	if err != nil {
		return err
	}

	// An org does not have a parent; its orgID equals its node ID. Looking up
	// an existing org by slug in node_by_property requires an orgID from the
	// user's existing membership. First-time seeding creates an org when
	// the user does not have any memberships.
	existingOrgIDs, memberErr := members.ListOrgIDsForUser(ctx, u.ID)
	if memberErr != nil {
		log.WarnContext(ctx, "seed.list_memberships_failed", "err", memberErr)
	}

	var knownOrgID uuid.UUID
	if len(existingOrgIDs) > 0 {
		knownOrgID = existingOrgIDs[0]
	}

	orgID, err := ensureSeedOrg(ctx, fdbStores, seeder, cfg, knownOrgID)
	if err != nil {
		return err
	}

	if err := seedMembership(ctx, members, orgID, u.ID); err != nil {
		return err
	}

	// orgID specifies both the workspace's parent and its lookup organization.
	wsID, err := ensureNode(
		ctx,
		fdbStores,
		"workspace",
		cfg.SeedWorkspaceSlug,
		cfg.SeedWorkspaceName,
		orgID,
		orgID,
	)
	if err != nil {
		return err
	}

	// Production authentication looks up the bearer's SHA-256 hash in api_tokens.
	// ENV=development uses the raw user UUID without a token lookup.
	// Print both credentials for selecting the one the environment requires.
	if err := seedToken(ctx, cfg, tokenRepo, u, orgID); err != nil {
		return err
	}

	log.InfoContext(ctx, "seed.completed", "user_id", u.ID, "org_id", orgID, "workspace_id", wsID)

	return nil
}

// ensureSeedOrg returns the org that the node_by_property index of knownOrgID
// lists for the seed slug, or the ID of a new org. It seeds the org's node
// types and property definitions with SeedOrg before it creates a new org
// node. After strict validation activates, CreateAtomic validates the org node
// against the node type org of that org and refuses it when the type does not
// exist.
func ensureSeedOrg(
	ctx context.Context,
	s *fdbadapter.Stores,
	seeder *service.Seeder,
	cfg *config.Config,
	knownOrgID uuid.UUID,
) (uuid.UUID, error) {
	existing, err := findSeedNode(ctx, s, "org", cfg.SeedOrgSlug, knownOrgID)
	if err != nil {
		return uuid.Nil, err
	}

	orgID := existing
	if orgID == uuid.Nil {
		orgID = seedNodeID("org", cfg.SeedOrgSlug, uuid.Nil)
	}

	if err := seeder.SeedOrg(ctx, orgID); err != nil {
		slog.ErrorContext(ctx, "seed.org_definitions_failed", slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("seed: seed org definitions: %w", err)
	}

	if existing != uuid.Nil {
		return existing, nil
	}

	if err := createSeedNode(
		ctx,
		s,
		orgID,
		"org",
		cfg.SeedOrgSlug,
		cfg.SeedOrgName,
		uuid.Nil,
	); err != nil {
		return uuid.Nil, err
	}

	return orgID, nil
}
