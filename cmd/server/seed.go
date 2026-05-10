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
// namespaces (orgNamespace, workspaceNamespace, systemPropNS) on purpose so
// a slug and an email can never collide on the same UUID.
var userIDNamespace = uuid.MustParse("0a6f7572-cafe-dead-beef-000000000003")

// userIDForEmail derives a deterministic UUID for a seed user from their email.
// Lowercased so case differences in re-seed inputs do not produce drift.
func userIDForEmail(email string) uuid.UUID {
	return uuid.NewSHA1(userIDNamespace, []byte(strings.ToLower(email)))
}

// runSeed creates the initial user, org, and workspace using the generic Node
// primitives. This is the one place in the system that references specific
// NodeType names (via service.Seeder constants).
func runSeed(cfg *config.Config) {
	_ = cfg // unused while seed is disabled per TACK-230; remove with the guard
	// Seed is disabled until TACK-230 lands. The current OrgID derivation at
	// internal/domain/node/types.go:288 hashes a slug into a deterministic UUID
	// without any tenant context, which means two tenants picking the same slug
	// produce byte-identical orgIDs and collide in FDB. Running seed against any
	// environment that already has data risks the same parallel-org failure mode
	// that took production down on 2026-05-09. TACK-230 replaces the hashed
	// derivation with random UUIDv7 plus an explicit override path for tests, at
	// which point this guard goes away.
	slog.Error("seed.disabled",
		slog.String("err", "seed disabled until TACK-230 fixes OrgID derivation"),
		slog.String("ticket", "TACK-230"),
		slog.String("incident", "2026-05-09 parallel-org outage"))
	os.Exit(1)

	if cfg.SeedEmail == "" || cfg.SeedName == "" {
		slog.Error("seed.config_missing",
			slog.String("err", "SEED_EMAIL and SEED_NAME are both required"))
		os.Exit(1)
	}

	// Seed runs without audit emission: it produces hundreds of node.create
	// events with a system-shaped actor that aren't user actions. The
	// suppression marker propagates through the same Recorder the running
	// server would use, so the wiring is already correct.
	ctx := audit.WithSuppressed(context.Background())
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
	orgID := ensureNode(ctx, fdbStores, "org", cfg.SeedOrgSlug, cfg.SeedOrgName, uuid.Nil)
	seeder.SeedOrg(ctx, orgID)

	// Make sure the user is an org admin.
	if err := members.AddMember(ctx, &org.Member{
		OrgID:  orgID,
		UserID: u.ID,
		Role:   20,
	}); err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		log.Warn("seed: add org member", "err", err)
	}

	// ── Workspace ──────────────────────────────────────────────────────────────
	wsID := ensureNode(ctx, fdbStores, "workspace", cfg.SeedWorkspaceSlug, cfg.SeedWorkspaceName, orgID)

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

// ensureNode creates (or re-verifies) a generic node with a slug index. When a
// node with (typeKey, slug) already exists, its ID is returned and no write
// happens beyond the slug index refresh. parentID may be uuid.Nil for org-level
// nodes.
func ensureNode(ctx context.Context, s *fdbadapter.Stores, typeKey, slug, name string, parentID uuid.UUID) uuid.UUID {
	log := telemetry.L(ctx)
	if existing, err := s.Nodes.GetAddress(ctx, typeKey, node.AddressKindPrimary, slug); err == nil && existing != uuid.Nil {
		log.Info("seed: node exists", "type", typeKey, "slug", slug, "id", existing)
		return existing
	}

	now := time.Now().UTC()

	// Deterministic ID derivation for the seed root nodes. A wipe and reseed
	// with the same slug produces byte-identical UUIDs, which keeps NodeType
	// and PropertyDef IDs stable across wipes.
	//
	// Root (no parent): the org node owns its slug. ID derives from the slug
	// alone. Workspace under org: ID derives from (orgID, slug).
	// Anything deeper still uses V7 because deeper seed nodes are not part of
	// the determinism contract today.
	var id uuid.UUID
	switch {
	case parentID == uuid.Nil:
		id = node.OrgID(slug)
	case typeKey == "workspace":
		id = node.WorkspaceID(parentID, slug)
	default:
		id = uuid.Must(uuid.NewV7())
	}

	props := make(map[string]json.RawMessage)
	slugRaw, _ := json.Marshal(slug)
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

	// Mark slug as indexed so ListByProperty works for it.
	if err := s.Nodes.CreateAtomic(ctx, n, view, rels, []string{"slug"}, nil); err != nil {
		log.Error("seed: create node", "type", typeKey, "err", err)
		os.Exit(1)
	}
	if err := s.Nodes.WriteAddress(ctx, typeKey, node.AddressKindPrimary, slug, id); err != nil {
		log.Error("seed: write slug", "type", typeKey, "err", err)
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
