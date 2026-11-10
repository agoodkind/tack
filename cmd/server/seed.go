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

// seedOp seeds the initial user, organization, workspace, and API token. It
// refuses to run when organization memberships already exist unless
// --allow-reseed permits reseeding.
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

// execSeed creates the initial user, organization, and workspace with
// generic Node operations and service.Seeder definitions. It returns fatal
// errors to the caller.
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

	// An organization node does not have a parent and uses its own ID as orgID.
	// The organization-scoped node_by_property index requires a known orgID for
	// a slug lookup. An existing user membership supplies that ID. The first
	// seed creates an organization when the user does not have a membership.
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

	// orgID supplies both the workspace's parent ID and its lookup organization
	// ID.
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

	// Production authentication looks up the bearer's SHA-256 hash in
	// api_tokens. With ENV=development, authentication uses the raw user UUID
	// without an api_tokens lookup. seedToken prints both forms for the
	// operator to select.
	if err := seedToken(ctx, cfg, tokenRepo, u, orgID); err != nil {
		return err
	}

	log.InfoContext(ctx, "seed.completed", "user_id", u.ID, "org_id", orgID, "workspace_id", wsID)

	return nil
}

// ensureSeedOrg returns the organization ID from knownOrgID's
// node_by_property index for the seed slug, or creates an organization. It
// uses SeedOrg to install node types and property definitions before
// creating the node. When strict validation is active, CreateAtomic checks
// the node against that organization's org node type and refuses a missing
// type.
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
