package datagen

import (
	"context"
	"errors"
	"log/slog"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/runtime"
)

const searchVerificationScale = "small"

// errPublicSearchDisabled reports that tack_search in this environment
// returns its fixed unavailable response.
var errPublicSearchDisabled = errors.New("public search is disabled; set OPENSEARCH_PUBLIC_ENABLED=true in this environment")

// SearchVerification reports the verified property warning case and relevance probe.
type SearchVerification struct {
	PropertyWarning SearchWarningCase    `json:"property_warning"`
	Relevance       SearchRelevanceProbe `json:"relevance"`
}

// VerifySearch runs VerifySearchWithSeed with the current Unix time in
// nanoseconds as the seed. The seed determines the organization ID and
// workspace slug.
func VerifySearch(ctx context.Context, cfg *config.Config) (SearchVerification, error) {
	return VerifySearchWithSeed(ctx, cfg, clock.Now().UnixNano())
}

// VerifySearchWithSeed checks the target marker and public search setting
// first. It then writes one organization for seed, opaque search metadata,
// and nodes, and calls tack_search through authenticated MCP requests.
// It returns a SearchVerification report with the property warning case and relevance probe.
func VerifySearchWithSeed(ctx context.Context, cfg *config.Config, seed int64) (SearchVerification, error) {
	var report SearchVerification
	if err := ValidateTarget(cfg); err != nil {
		return report, loggedError(ctx, "qa datagen: validate search target", err)
	}
	if !cfg.SearchPublicEnabled {
		return report, loggedError(ctx, "qa datagen: verify search", errPublicSearchDisabled)
	}
	limits, err := config.LoadSearchQuerySettings(ctx)
	if err != nil {
		return report, loggedError(ctx, "qa datagen: load search settings", err)
	}
	phrases, err := loadSearchPhrases(ctx)
	if err != nil {
		return report, err
	}
	scale, err := ParseScale(searchVerificationScale)
	if err != nil {
		return report, err
	}
	graph, err := runtime.BuildGraph(ctx, cfg)
	if err != nil {
		return report, loggedError(ctx, "qa datagen: build runtime for search verification", err)
	}
	defer graph.Close()
	graph.StartSearchWorkers(ctx)
	identities, err := BootstrapIdentities(ctx, cfg, seed, scale)
	if err != nil {
		return report, err
	}
	stores, members, closeMembers, err := openSearchFixtureStores(ctx, cfg, "search verification")
	if err != nil {
		return report, err
	}
	defer closeMembers()
	workspace := identities.Workspaces[0]
	fixture, err := newSearchFixture(ctx, stores, members, workspace)
	if err != nil {
		return report, err
	}
	run := searchRun{
		driver: NewDriver(graph, false, seed), token: workspace.Actors[0].Token, entry: workspace.Slug,
		limits:  searchLimits{MaxResults: limits.MaxResults, MaxResponseBytes: limits.MaxResponseBytes},
		fixture: fixture, phrases: phrases, pages: graph,
	}
	if err := run.verify(ctx, workspace); err != nil {
		return report, err
	}
	if report.PropertyWarning, err = run.verifyPropertyWarning(ctx); err != nil {
		return report, err
	}
	if report.Relevance, err = run.probeRelevance(ctx); err != nil {
		return report, err
	}
	if err := run.verifyAccess(ctx, cfg, seed, workspace); err != nil {
		return report, err
	}
	slog.InfoContext(ctx, "qa.datagen.search_verified", slog.String("workspace", workspace.Slug),
		slog.String("org_id", workspace.OrgID.String()), slog.Int64("seed", seed))
	return report, nil
}
