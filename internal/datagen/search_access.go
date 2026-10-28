package datagen

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
)

const (
	// Both organizations store accessPhrase. Authorization is the only
	// difference between their results.
	accessPhrase = "umber lantern access ledger"
	// accessCallerNodes exceeds one public page of 25 nodes. The first page
	// of the caller organization returns a continuation cursor.
	accessCallerNodes  = 30
	accessForeignNodes = 3
	// foreignSeedOffset derives the foreign organization from the
	// verification seed.
	foreignSeedOffset = 1
	// revokedActorIndex selects the actor that the revocation check removes
	// from the organization. Actor 0 runs every other check.
	revokedActorIndex = 2
)

// verifyAccess requires tack_search to return only nodes that the caller's
// current membership allows. It bootstraps a second organization with
// matching text, refuses each actor under the other organization's entry
// node, replays one cursor, and refuses a member after removal.
func (r searchRun) verifyAccess(ctx context.Context, cfg *config.Config, seed int64, workspace WorkspaceIdentity) error {
	if len(workspace.Actors) <= revokedActorIndex {
		return fmt.Errorf("qa datagen: workspace %s has %d actors, want more than %d", workspace.Slug, len(workspace.Actors), revokedActorIndex)
	}
	scale, err := ParseScale(searchVerificationScale)
	if err != nil {
		return err
	}
	foreign, err := BootstrapIdentities(ctx, cfg, seed+foreignSeedOffset, scale)
	if err != nil {
		return err
	}
	foreignWorkspace := foreign.Workspaces[0]
	foreignFixture, err := newSearchFixture(ctx, r.fixture.stores, r.fixture.members, foreignWorkspace)
	if err != nil {
		return err
	}
	// Both runs use one registry to resolve either organization's results to
	// node IDs for the isolation checks.
	foreignFixture.titles = r.fixture.titles
	foreignEntry, err := r.fixture.stores.Views.Get(ctx, foreignFixture.entryID)
	if err != nil || foreignEntry == nil {
		return loggedError(ctx, "qa datagen: read foreign entry node "+foreignFixture.entryID.String(), errMissing(err))
	}
	foreignFixture.titles.Add(SearchItem{Title: foreignWorkspace.Slug, NodeType: foreignEntry.NodeType}, foreignFixture.entryID)
	callerNodes, err := putAccessNodes(ctx, r.fixture, "caller access case ", accessCallerNodes)
	if err != nil {
		return err
	}
	foreignNodes, err := putAccessNodes(ctx, foreignFixture, "foreign access case ", accessForeignNodes)
	if err != nil {
		return err
	}
	foreignRun := r
	foreignRun.token, foreignRun.entry, foreignRun.fixture = foreignWorkspace.Actors[0].Token, foreignWorkspace.Slug, foreignFixture
	checks := []struct {
		description string
		check       func(context.Context) error
	}{
		{"isolate caller results", func(ctx context.Context) error {
			return r.isolated(ctx, callerNodes, foreignNodes, foreignFixture.orgID)
		}},
		{"isolate foreign results", func(ctx context.Context) error {
			return foreignRun.isolated(ctx, foreignNodes, callerNodes, r.fixture.orgID)
		}},
	}
	for _, step := range checks {
		if err := r.eventually(ctx, step.description, step.check); err != nil {
			return err
		}
	}
	if err := r.refused(ctx, foreignRun.token, r.entry, ""); err != nil {
		return loggedError(ctx, "qa datagen: refuse foreign actor", err)
	}
	if err := r.refused(ctx, r.token, foreignRun.entry, ""); err != nil {
		return loggedError(ctx, "qa datagen: refuse caller under foreign entry", err)
	}
	if err := r.verifyReplay(ctx); err != nil {
		return err
	}
	if err := r.verifyRevocation(ctx, cfg, workspace); err != nil {
		return err
	}
	slog.InfoContext(ctx, "qa.datagen.search_access_verified", slog.String("workspace", workspace.Slug),
		slog.String("foreign_workspace", foreignWorkspace.Slug), slog.Int("caller_nodes", len(callerNodes)),
		slog.Int("foreign_nodes", len(foreignNodes)))
	return nil
}

// putAccessNodes creates count nodes that store accessPhrase.
func putAccessNodes(ctx context.Context, fixture searchFixture, namePrefix string, count int) ([]uuid.UUID, error) {
	nodes := make([]uuid.UUID, 0, count)
	for number := range count {
		nodeID, err := fixture.put(ctx, namePrefix+strconv.Itoa(number), accessPhrase, "")
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, nodeID)
	}
	return nodes, nil
}

// isolated requires every allowed node and rejects every forbidden node
// across all search pages for accessPhrase. It checks each registered node's
// organization through its resolve record. It accepts an unregistered result
// only when the caller organization declares its node type and the other
// organization does not.
func (r searchRun) isolated(ctx context.Context, allowed, forbidden []uuid.UUID, otherOrgID uuid.UUID) error {
	results, err := traverseSearch(ctx, r.driver, r.fixture.titles, r.token, r.entry, accessPhrase, r.limits)
	if err != nil {
		return err
	}
	for _, nodeID := range allowed {
		if !slices.Contains(results, nodeID) {
			return fmt.Errorf("search %q under %s omitted node %s", accessPhrase, r.entry, nodeID)
		}
	}
	for _, nodeID := range forbidden {
		if slices.Contains(results, nodeID) {
			return fmt.Errorf("search %q under %s returned node %s of another organization", accessPhrase, r.entry, nodeID)
		}
	}
	ownedTypes, err := r.fixture.typeKeys(ctx, r.fixture.orgID)
	if err != nil {
		return err
	}
	otherTypes, err := r.fixture.typeKeys(ctx, otherOrgID)
	if err != nil {
		return err
	}
	for _, nodeID := range results {
		// Every organization declares the built-in node types. A built-in type
		// cannot establish the organization of an unregistered result.
		if item, unresolved := r.fixture.titles.Unresolved(nodeID); unresolved {
			if !slices.Contains(ownedTypes, item.NodeType) || slices.Contains(otherTypes, item.NodeType) {
				return fmt.Errorf("search %q under %s returned the unregistered result %q of type %q, which only org %s must declare",
					accessPhrase, r.entry, item.Title, item.NodeType, r.fixture.orgID)
			}
			continue
		}
		resolved, err := r.fixture.stores.Views.Resolve(ctx, nodeID)
		if err != nil {
			return loggedError(ctx, "qa datagen: resolve search result "+nodeID.String()+" under "+r.entry, err)
		}
		if resolved.OrgID != r.fixture.orgID {
			return fmt.Errorf("search %q under %s returned node %s of org %s, want org %s",
				accessPhrase, r.entry, nodeID, resolved.OrgID, r.fixture.orgID)
		}
	}
	return nil
}

// refused requires one tack_search call to fail with an authorization
// refusal that isAuthorizationRefusal accepts. The call reads the entry
// argument name from the caller's tools/list schema first, and any failure of
// that read fails the check. A result page, any other tool error, or a
// transport failure also fails the check.
func (r searchRun) refused(ctx context.Context, token, entry, cursor string) error {
	argument, err := r.driver.searchEntryArgument(ctx, token)
	if err != nil {
		return loggedError(ctx, "qa datagen: read the tack_search schema of a caller under "+entry, err)
	}
	arguments := map[string]string{argument: entry, "query": accessPhrase}
	if cursor != "" {
		arguments["cursor"] = cursor
	}
	_, err = r.driver.callSearchArguments(ctx, token, arguments)
	if err == nil {
		return fmt.Errorf("search under %s with argument %s returned a page to a caller without access", entry, argument)
	}
	if isAuthorizationRefusal(err, entry) {
		return nil
	}
	return loggedError(ctx, "qa datagen: search under "+entry+" failed without an authorization refusal", err)
}
