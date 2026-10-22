package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/org"
)

const (
	twoProcessEndpointCount = 2
	twoProcessNodeCount     = 80
	twoProcessNamePrefix    = "two process case "
)

// VerifySearchTwoProcess checks search continuation, replay, and revocation across
// two remote Tack processes in an isolated organization.
func VerifySearchTwoProcess(ctx context.Context, cfg *config.Config, endpoints []string, client *http.Client) error {
	if len(endpoints) != twoProcessEndpointCount {
		return loggedError(ctx, "qa datagen: verify two-process search", fmt.Errorf("got %d endpoints, want %d", len(endpoints), twoProcessEndpointCount))
	}
	if err := ValidateTarget(cfg); err != nil {
		return loggedError(ctx, "qa datagen: validate search target", err)
	}
	if !cfg.SearchPublicEnabled {
		return loggedError(ctx, "qa datagen: verify two-process search", errPublicSearchDisabled)
	}
	limits, err := config.LoadSearchQuerySettings(ctx)
	if err != nil {
		return loggedError(ctx, "qa datagen: load search settings", err)
	}
	scale, err := ParseScale(searchVerificationScale)
	if err != nil {
		return err
	}
	seed := clock.Now().UnixNano()
	drivers := make([]*Driver, 0, len(endpoints))
	for _, endpoint := range endpoints {
		driver, err := NewRemoteDriver(endpoint, client, seed)
		if err != nil {
			return loggedError(ctx, "qa datagen: build remote driver for "+endpoint, err)
		}
		drivers = append(drivers, driver)
	}
	identities, err := BootstrapIdentities(ctx, cfg, seed, scale)
	if err != nil {
		return err
	}
	stores, err := fdbadapter.NewStores(cfg.FDBClusterFile, cfg.FDBTransactionTimeout, nil)
	if err != nil {
		return loggedError(ctx, "qa datagen: open foundationdb for two-process search", err)
	}
	stores.EnableSearchWork()
	workspace := identities.Workspaces[0]
	if len(workspace.Actors) <= revokedActorIndex {
		return loggedError(ctx, "qa datagen: verify two-process search", fmt.Errorf("workspace %s has %d actors", workspace.Slug, len(workspace.Actors)))
	}
	fixture, err := newSearchFixture(ctx, stores, workspace)
	if err != nil {
		return err
	}
	nodes, err := putAccessNodes(ctx, fixture, twoProcessNamePrefix, twoProcessNodeCount)
	if err != nil {
		return err
	}
	run := searchRun{
		driver: drivers[0], token: workspace.Actors[0].Token, entry: workspace.Slug,
		limits:  searchLimits{MaxResults: limits.MaxResults, MaxResponseBytes: limits.MaxResponseBytes},
		fixture: fixture, phrases: searchPhrases{}, pages: nil,
	}
	indexed := func(ctx context.Context) error { return run.includes(ctx, accessPhrase, nodes...) }
	if err := run.eventually(ctx, "index two-process nodes", indexed); err != nil {
		return err
	}
	baseline, err := traverseSearch(ctx, drivers[0], run.token, run.entry, accessPhrase, run.limits)
	if err != nil {
		return err
	}
	alternated, err := run.alternate(ctx, drivers)
	if err != nil {
		return err
	}
	if !slices.Equal(baseline, alternated) {
		return loggedError(ctx, "qa datagen: alternate search endpoints", fmt.Errorf("alternating traversal returned %v, want %v", alternated, baseline))
	}
	if err := run.replayAcross(ctx, drivers); err != nil {
		return err
	}
	if err := run.revokeAcross(ctx, cfg, drivers, workspace); err != nil {
		return err
	}
	slog.InfoContext(ctx, "qa.datagen.search_two_process_verified", slog.String("workspace", workspace.Slug),
		slog.String("org_id", workspace.OrgID.String()), slog.Int64("seed", seed), slog.Int("nodes", len(baseline)))
	return nil
}

func (r searchRun) alternate(ctx context.Context, drivers []*Driver) ([]uuid.UUID, error) {
	seen := make(map[uuid.UUID]struct{})
	cursors := make(map[string]struct{})
	ordered := make([]uuid.UUID, 0)
	cursor := ""
	for pageIndex := range maxSearchTraversalPages {
		page, err := callSearch(ctx, drivers[pageIndex%len(drivers)], r.token, r.entry, accessPhrase, cursor)
		if err != nil {
			return nil, err
		}
		if len(page.IDs) > r.limits.MaxResults || page.ResponseBytes > r.limits.MaxResponseBytes {
			return nil, loggedError(ctx, "qa datagen: alternate search page bound", fmt.Errorf("page %d returned %d nodes in %d bytes, above %d nodes or %d bytes",
				pageIndex, len(page.IDs), page.ResponseBytes, r.limits.MaxResults, r.limits.MaxResponseBytes))
		}
		for _, nodeID := range page.IDs {
			if _, duplicate := seen[nodeID]; duplicate {
				return nil, loggedError(ctx, "qa datagen: alternate search duplicate", fmt.Errorf("node %s returned twice", nodeID))
			}
			seen[nodeID] = struct{}{}
			ordered = append(ordered, nodeID)
		}
		if page.Cursor == "" {
			return ordered, nil
		}
		if _, repeated := cursors[page.Cursor]; repeated {
			return nil, loggedError(ctx, "qa datagen: alternate search cursor", fmt.Errorf("page %d repeated a continuation cursor", pageIndex))
		}
		cursors[page.Cursor] = struct{}{}
		cursor = page.Cursor
	}
	return nil, loggedError(ctx, "qa datagen: alternate search", fmt.Errorf("traversal did not finish within %d pages", maxSearchTraversalPages))
}

func (r searchRun) replayAcross(ctx context.Context, drivers []*Driver) error {
	first, err := callSearch(ctx, drivers[0], r.token, r.entry, accessPhrase, "")
	if err != nil {
		return err
	}
	if first.Cursor == "" {
		return loggedError(ctx, "qa datagen: replay across endpoints", errors.New("first page returned no continuation cursor"))
	}
	second, err := callSearch(ctx, drivers[1], r.token, r.entry, accessPhrase, first.Cursor)
	if err != nil {
		return err
	}
	replayed, err := callSearch(ctx, drivers[0], r.token, r.entry, accessPhrase, first.Cursor)
	if err != nil {
		return err
	}
	if len(second.IDs) == 0 || !slices.Equal(second.IDs, replayed.IDs) || second.Cursor != replayed.Cursor {
		return loggedError(ctx, "qa datagen: replay across endpoints",
			fmt.Errorf("replayed page returned %v with cursor %q, want %v with cursor %q", replayed.IDs, replayed.Cursor, second.IDs, second.Cursor))
	}
	return nil
}

func (r searchRun) revokeAcross(ctx context.Context, cfg *config.Config, drivers []*Driver, workspace WorkspaceIdentity) error {
	actor := workspace.Actors[revokedActorIndex]
	first, err := callSearch(ctx, drivers[0], actor.Token, r.entry, accessPhrase, "")
	if err != nil {
		return err
	}
	if first.Cursor == "" {
		return loggedError(ctx, "qa datagen: revoke across endpoints", errors.New("member search returned no continuation cursor"))
	}
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, nil)
	if err != nil {
		return loggedError(ctx, "qa datagen: open postgres for revocation", err)
	}
	defer pool.Close()
	members := postgres.NewOrgMemberRepo(pool)
	member, err := findOrgMember(ctx, members, workspace.OrgID, actor.UserID)
	if err != nil {
		return err
	}
	if err := members.RemoveMember(ctx, workspace.OrgID, actor.UserID); err != nil {
		return loggedError(ctx, "qa datagen: remove member "+actor.UserID.String(), err)
	}
	// Restoring membership before both checks finish would invalidate the remaining revocation check.
	var refusalErr error
	for _, driver := range drivers {
		endpointRun := r
		endpointRun.driver = driver
		refusalErr = errors.Join(refusalErr, endpointRun.requireRevoked(ctx, actor.Token, first.Cursor))
	}
	restored := &org.Member{ID: uuid.Nil, OrgID: member.OrgID, UserID: member.UserID, Role: member.Role, CreatedAt: time.Time{}}
	if err := members.AddMember(context.WithoutCancel(ctx), restored); err != nil {
		joined := errors.Join(refusalErr, fmt.Errorf("qa datagen: restore member %s: %w", actor.UserID, err))
		slog.ErrorContext(ctx, "qa.datagen.member_restore_failed", slog.String("user_id", actor.UserID.String()),
			slog.String("err", joined.Error()))
		return joined
	}
	return refusalErr
}
