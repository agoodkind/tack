package datagen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
)

const (
	// accessPhrase is stored by both organizations. Only authorization
	// separates their results.
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
	// searchUnavailableResponse is the fixed tack_search outage text. An
	// outage must not pass as an authorization refusal.
	searchUnavailableResponse = "Search is temporarily unavailable."
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
	foreignFixture, err := newSearchFixture(ctx, r.fixture.stores, foreignWorkspace)
	if err != nil {
		return err
	}
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
		{"isolate caller results", func(ctx context.Context) error { return r.isolated(ctx, callerNodes, foreignNodes) }},
		{"isolate foreign results", func(ctx context.Context) error { return foreignRun.isolated(ctx, foreignNodes, callerNodes) }},
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

// isolated requires the complete traversal for accessPhrase to contain every
// allowed node and no forbidden node.
func (r searchRun) isolated(ctx context.Context, allowed, forbidden []uuid.UUID) error {
	results, err := traverseSearch(ctx, r.driver, r.token, r.entry, accessPhrase, r.limits)
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
	return nil
}

// refused requires one tack_search call to fail with an authorization
// error. A result page, the outage response, or a transport failure fails
// the check.
func (r searchRun) refused(ctx context.Context, token, entry, cursor string) error {
	page, err := callSearch(ctx, r.driver, token, entry, accessPhrase, cursor)
	if err == nil {
		return fmt.Errorf("search under %s returned %d nodes to a caller without access", entry, len(page.IDs))
	}
	if strings.Contains(err.Error(), searchUnavailableResponse) {
		return loggedError(ctx, "qa datagen: search under "+entry+" returned the outage response instead of a refusal", err)
	}
	var toolError *toolCallError
	if errors.As(err, &toolError) {
		return nil
	}
	var statusError *httpStatusError
	if errors.As(err, &statusError) && (statusError.statusCode == http.StatusUnauthorized || statusError.statusCode == http.StatusForbidden) {
		return nil
	}
	return loggedError(ctx, "qa datagen: search under "+entry+" failed without an authorization refusal", err)
}
