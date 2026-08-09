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
	clispec.InputMarker `exhaustruct:"optional"`
	AllowReseed         bool
}

// seedOp seeds the initial user, org, workspace, and API token. It refuses to
// run against a non-empty database unless --allow-reseed is passed.
func seedOp(f *cli.Factory) clispec.Operation[seedInput] {
	return clispec.Operation[seedInput]{
		Name: clispec.Name{Canonical: "seed"},
		Audit: audit.Spec{
			Verb: string(audit.VerbBootstrapSeed), Mutates: true,
		},
		Short: "Seed the initial user, org, workspace, and API token",
		Params: []clispec.Param[seedInput]{
			clispec.BoolParam("allow-reseed",
				"Allow seed against a non-empty database; required for re-seeding production",
				false, func(in *seedInput, v bool) { in.AllowReseed = v }),
		},
		New: func() seedInput { return seedInput{AllowReseed: false} },
		Run: func(ctx context.Context, in seedInput, sink clispec.ResultSink) error {
			nonEmpty, err := orgsExist(ctx, f.Cfg)
			if err != nil {
				return err
			}
			if nonEmpty && !in.AllowReseed {
				return errors.New("seed refused: database already contains at least one org; pass --allow-reseed to override (TACK-230, 2026-05-09 parallel-org outage)")
			}
			if f.Cfg.SeedEmail == "" || f.Cfg.SeedName == "" {
				return errors.New("seed: SEED_EMAIL and SEED_NAME are both required")
			}
			recorder := audit.CanonicalRecorder{Inner: seedOutboxRecorder{outbox: f.AuditOutbox()}}
			if err := execSeed(ctx, f.Cfg, recorder); err != nil {
				return err
			}
			return sink.WriteText(ctx, "seed complete")
		},
	}
}

// execSeed creates the initial user, org, and workspace using the generic Node
// primitives. This is the one place in the system that references specific
// NodeType names (via service.Seeder constants). It returns any fatal error to
// the caller rather than exiting in place.
func execSeed(ctx context.Context, cfg *config.Config, recorder audit.Recorder) error {
	ctx, span := telemetry.StartSpan(ctx, "seed.run", trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.String("command", "seed"))
	log := telemetry.L(ctx)

	if err := postgres.Migrate(ctx, cfg.DatabaseURL, migrations.FS); err != nil {
		slog.ErrorContext(ctx, "seed.migrate_failed", slog.String("err", err.Error()))
		return fmt.Errorf("seed: migrate: %w", err)
	}

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		slog.ErrorContext(ctx, "seed.postgres_failed", slog.String("err", err.Error()))
		return fmt.Errorf("seed: postgres: %w", err)
	}
	defer pool.Close()

	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile, pool)
	if err != nil {
		slog.ErrorContext(ctx, "seed.foundationdb_failed", slog.String("err", err.Error()))
		return fmt.Errorf("seed: foundationdb: %w", err)
	}

	userRepo := postgres.NewUserRepo(pool)
	tokenRepo := postgres.NewTokenRepo(pool)
	members := postgres.NewOrgMemberRepo(pool)
	seeder := service.NewSeeder(
		seedPropertyDefs{inner: fdbStores.PropertyDefs, recorder: recorder},
		seedNodeTypes{inner: fdbStores.NodeTypes, recorder: recorder},
	)

	u, err := userRepo.GetByEmail(ctx, cfg.SeedEmail)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		slog.ErrorContext(ctx, "seed.get_user_failed", slog.String("err", err.Error()))
		return fmt.Errorf("seed: get user: %w", err)
	}
	if u == nil {
		u, err = userRepo.Create(ctx, &user.User{
			ID:          userIDForEmail(cfg.SeedEmail),
			Email:       cfg.SeedEmail,
			DisplayName: cfg.SeedName,
		})
		if err != nil {
			slog.ErrorContext(ctx, "seed.create_user_failed", slog.String("err", err.Error()))
			return fmt.Errorf("seed: create user: %w", err)
		}
		if err := recordSeedUser(ctx, recorder, u); err != nil {
			return err
		}
		log.InfoContext(ctx, "seed.user_created", "id", u.ID, "email", u.Email)
	} else {
		log.InfoContext(ctx, "seed.user_exists", "id", u.ID, "email", u.Email)
	}

	// The org case has no parent, so orgID == the node's own ID. To detect an
	// existing org by slug via the org-scoped node_by_property index, we need a
	// known orgID, derived from the user's existing org membership (if any). On
	// a first seed the membership list is empty and a new org is created.
	existingOrgIDs, memberErr := members.ListOrgIDsForUser(ctx, u.ID)
	if memberErr != nil {
		log.WarnContext(ctx, "seed.list_memberships_failed", "err", memberErr)
	}
	var knownOrgID uuid.UUID
	if len(existingOrgIDs) > 0 {
		knownOrgID = existingOrgIDs[0]
	}
	orgID, orgCreated, err := ensureNode(ctx, fdbStores, "org", cfg.SeedOrgSlug, cfg.SeedOrgName, uuid.Nil, knownOrgID)
	if err != nil {
		return err
	}
	if orgCreated {
		if err := recordSeedNode(ctx, recorder, orgID, orgID, uuid.Nil, "org", cfg.SeedOrgSlug, cfg.SeedOrgName); err != nil {
			return err
		}
	}
	if err := seeder.SeedOrg(ctx, orgID); err != nil {
		log.ErrorContext(ctx, "seed.org_definitions_failed", slog.String("err", err.Error()))
		return fmt.Errorf("seed: seed org definitions: %w", err)
	}

	if err := members.AddMember(ctx, &org.Member{OrgID: orgID, UserID: u.ID, Role: 20}); err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		log.WarnContext(ctx, "seed.add_member_failed", "err", err)
	}

	// orgID is the parent of the workspace, so it doubles as the lookup orgID.
	wsID, workspaceCreated, err := ensureNode(ctx, fdbStores, "workspace", cfg.SeedWorkspaceSlug, cfg.SeedWorkspaceName, orgID, orgID)
	if err != nil {
		return err
	}
	if workspaceCreated {
		if err := recordSeedNode(ctx, recorder, wsID, orgID, orgID, "workspace", cfg.SeedWorkspaceSlug, cfg.SeedWorkspaceName); err != nil {
			return err
		}
	}

	// Production mode: SHA-256 hash of the bearer is looked up in api_tokens.
	// Dev mode (ENV=development): the bearer is the raw user UUID directly, no
	// api_tokens lookup. Print both so the operator picks the right one.
	if err := seedToken(ctx, cfg, tokenRepo, u, orgID, recorder); err != nil {
		return err
	}

	log.InfoContext(ctx, "seed.completed", "user_id", u.ID, "org_id", orgID, "workspace_id", wsID)
	return nil
}
