package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/migrations"
)

// userIDNamespace gives the seed user a deterministic UUID derived from email.
// The dev-mode auth path uses the raw user UUID as a Bearer token, so a stable
// UUID across Yugabyte wipes means a stable MCP config that doesn't rebreak
// every time we re-seed. The namespace is distinct from the node-side
// namespaces (workspaceNamespace, systemPropNS) on purpose so a slug and an
// email can never collide on the same UUID.
var userIDNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000003")

// userIDForEmail derives a deterministic UUID for a seed user from their email.
// Lowercased so case differences in re-seed inputs do not produce drift.
func userIDForEmail(email string) uuid.UUID {
	return uuid.NewSHA1(userIDNamespace, []byte(strings.ToLower(email)))
}

// runSeed enforces the reseed guard and delegates to execSeed. It is the
// entry point for the seed subcommand.
func runSeed(cfg *config.Config) {
	allowReseed := flag.Bool("allow-reseed", false,
		"Allow seed against a non-empty database. Required for re-seeding production. "+
			"Without this flag, seed refuses if any org already exists.")
	flag.Parse()

	ctx := context.Background()
	nonEmpty, err := orgsExist(ctx, cfg)
	if err != nil {
		slog.Error("seed.precheck_failed", slog.Any("err", err))
		os.Exit(1)
	}
	if nonEmpty && !*allowReseed {
		slog.Error("seed.refused",
			slog.String("err", "database already contains at least one org; pass --allow-reseed to override"),
			slog.String("ticket", "TACK-230"),
			slog.String("incident", "2026-05-09 parallel-org outage"))
		os.Exit(1)
	}

	if cfg.SeedEmail == "" || cfg.SeedName == "" {
		slog.Error("seed.config_missing",
			slog.String("err", "SEED_EMAIL and SEED_NAME are both required"))
		os.Exit(1)
	}

	execSeed(ctx, cfg)
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

	// ── User ───────────────────────────────────────────────────────────────────
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

	// ── Org ────────────────────────────────────────────────────────────────────
	// The org case has no parent, so orgID == the node's own ID. To detect an
	// existing org by slug via the org-scoped node_by_property index, we need
	// a known orgID. We derive it from the user's existing org membership (if
	// any). On a first seed the membership list is empty and we create a new org.
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

	// Make sure the user is an org admin.
	if err := members.AddMember(ctx, &org.Member{
		OrgID:  orgID,
		UserID: u.ID,
		Role:   20,
	}); err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		log.WarnContext(ctx, "seed: add org member", "err", err)
	}

	// ── Workspace ──────────────────────────────────────────────────────────────
	// orgID is the parent of the workspace, so it doubles as the lookup orgID.
	wsID := ensureNode(ctx, fdbStores, "workspace", cfg.SeedWorkspaceSlug, cfg.SeedWorkspaceName, orgID, orgID)

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
		log.Info("seed: prod token already exists, skipping")
	} else {
		_, _ = fmt.Fprintf(os.Stdout, "\nProduction-mode API token (copy now; not shown again):\n  %s\n", raw)
	}
	_, _ = fmt.Fprintf(os.Stdout, "\nDev-mode bearer (stable across wipes, derived from %s):\n  %s\n\n", cfg.SeedEmail, u.ID)
	_, _ = fmt.Fprintln(os.Stdout, "Add to your MCP config:")
	_, _ = fmt.Fprintln(os.Stdout, "  \"Authorization\": \"Bearer <token-above>\"")

	log.Info("seed complete",
		"user_id", u.ID,
		"org_id", orgID,
		"workspace_id", wsID,
	)
}

// ensureNode creates (or re-verifies) a generic node. When a node with
// (typeKey, slug) already exists it is detected via the org-scoped
// node_by_property index and its ID is returned with no further writes.
// parentID may be uuid.Nil for org-level nodes. lookupOrgID is the orgID used
// to query the property index for an existing node: for non-org nodes this is
// the parentID; for org nodes (where orgID == the node's own ID) the caller
// supplies the orgID from the user's existing org membership, or uuid.Nil when
// no prior membership exists.
func ensureNode(ctx context.Context, s *fdbadapter.Stores, typeKey, slug, name string, parentID, lookupOrgID uuid.UUID) uuid.UUID {
	log := telemetry.L(ctx)
	slugRaw, _ := json.Marshal(slug)
	if lookupOrgID != uuid.Nil {
		existing, err := s.Nodes.ListByProperty(ctx, lookupOrgID, typeKey, "slug", slugRaw)
		if err == nil && len(existing) > 0 {
			log.Info("seed: node exists", "type", typeKey, "slug", slug, "id", existing[0].ID)
			return existing[0].ID
		}
	}

	now := time.Now().UTC()

	// Org nodes get a fresh random UUIDv7 (TACK-230). Two calls with the same
	// slug produce different IDs, which is the correct behaviour: org identity
	// is row-level, not derivable from a slug.
	//
	// Workspace under org: ID derives from (orgID, slug) and remains
	// deterministic so a wipe-and-reseed with the same org + workspace slug
	// produces byte-identical workspace IDs. Anything deeper uses V7.
	var id uuid.UUID
	switch {
	case parentID == uuid.Nil:
		id = node.NewOrgID()
	case typeKey == "workspace":
		id = node.WorkspaceID(parentID, slug)
	default:
		id = uuid.Must(uuid.NewV7())
	}

	props := make(map[string]json.RawMessage)
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

	// Mark slug as indexed so ListByProperty works for it. The node_by_property
	// index written here replaces the former address_index write; no separate
	// WriteAddress call is needed.
	if err := s.Nodes.CreateAtomic(ctx, n, view, rels, []string{"slug"}, nil); err != nil {
		log.Error("seed: create node", "type", typeKey, "err", err)
		os.Exit(1)
	}
	log.Info("seed: created node", "type", typeKey, "slug", slug, "id", id)
	return id
}

func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("seed: generate token: " + err.Error())
	}
	return "tack_" + base64.RawURLEncoding.EncodeToString(b)
}

// orgsExist opens a read-only Postgres connection and returns true if
// org_members contains at least one row, which implies at least one org has
// been seeded. It uses org_members rather than an entity table because SQL
// holds the only authoritative org registry (FDB holds product data, not
// auth). If the table does not yet exist (pre-migration), it returns false so
// seed can proceed to run migrations first.
func orgsExist(ctx context.Context, cfg *config.Config) (bool, error) {
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, nil)
	if err != nil {
		slog.Error("seed.orgs_exist.open_db", slog.Any("err", err))
		return false, fmt.Errorf("orgsExist: open db: %w", err)
	}
	defer pool.Close()

	var exists bool
	err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM org_members LIMIT 1)`).Scan(&exists)
	if err != nil {
		// Table does not exist yet (pre-migration); treat as empty.
		if strings.Contains(err.Error(), "does not exist") {
			return false, nil
		}
		slog.Error("seed.orgs_exist.query", slog.Any("err", err))
		return false, fmt.Errorf("orgsExist: query: %w", err)
	}
	return exists, nil
}
