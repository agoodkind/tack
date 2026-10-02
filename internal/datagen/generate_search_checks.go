package datagen

import (
	"context"
	"errors"
	"log/slog"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/runtime"
)

const searchVerificationScale = "small"

// errPublicSearchDisabled reports that tack_search in this environment
// returns its fixed unavailable response.
var errPublicSearchDisabled = errors.New("public search is disabled; set OPENSEARCH_PUBLIC_ENABLED=true in this environment")

// VerifySearch runs VerifySearchWithSeed with the current Unix time in
// nanoseconds as the seed. The seed determines the organization ID and
// workspace slug.
func VerifySearch(ctx context.Context, cfg *config.Config) error {
	return VerifySearchWithSeed(ctx, cfg, clock.Now().UnixNano())
}

// VerifySearchWithSeed checks the target marker and public search setting
// first. It then writes one organization for seed, opaque search metadata,
// and nodes, and calls tack_search through authenticated MCP requests.
func VerifySearchWithSeed(ctx context.Context, cfg *config.Config, seed int64) error {
	if err := ValidateTarget(cfg); err != nil {
		return loggedError(ctx, "qa datagen: validate search target", err)
	}
	if !cfg.SearchPublicEnabled {
		return loggedError(ctx, "qa datagen: verify search", errPublicSearchDisabled)
	}
	limits, err := config.LoadSearchQuerySettings(ctx)
	if err != nil {
		return loggedError(ctx, "qa datagen: load search settings", err)
	}
	phrases, err := loadSearchPhrases(ctx)
	if err != nil {
		return err
	}
	scale, err := ParseScale(searchVerificationScale)
	if err != nil {
		return err
	}
	graph, err := runtime.BuildGraph(ctx, cfg)
	if err != nil {
		return loggedError(ctx, "qa datagen: build runtime for search verification", err)
	}
	defer graph.Close()
	graph.StartSearchWorkers(ctx)
	identities, err := BootstrapIdentities(ctx, cfg, seed, scale)
	if err != nil {
		return err
	}
	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, nil)
	if err != nil {
		return loggedError(ctx, "qa datagen: open foundationdb for search verification", err)
	}
	stores.EnableSearchWork()
	workspace := identities.Workspaces[0]
	fixture, err := newSearchFixture(ctx, stores, workspace)
	if err != nil {
		return err
	}
	run := searchRun{
		driver: NewDriver(graph, false, seed), token: workspace.Actors[0].Token, entry: workspace.Slug,
		limits:  searchLimits{MaxResults: limits.MaxResults, MaxResponseBytes: limits.MaxResponseBytes},
		fixture: fixture, phrases: phrases, pages: graph,
	}
	if err := run.verify(ctx, workspace); err != nil {
		return err
	}
	if err := run.verifyAccess(ctx, cfg, seed, workspace); err != nil {
		return err
	}
	slog.InfoContext(ctx, "qa.datagen.search_verified", slog.String("workspace", workspace.Slug),
		slog.String("org_id", workspace.OrgID.String()), slog.Int64("seed", seed))
	return nil
}
