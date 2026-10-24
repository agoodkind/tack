package integration_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/datagen"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
)

const (
	// rotatedFilterQuery is the query text for the allowed and forbidden node
	// corpora.
	rotatedFilterQuery = "saffron pipeline"
	// rotatedFilterAllowed sets the corpus size to five allowed nodes in the
	// caller's organization.
	rotatedFilterAllowed = 5
	// rotatedFilterForbidden sets the corpus size to 60 forbidden nodes in
	// another organization. These nodes have stronger lexical matches than
	// the allowed nodes.
	rotatedFilterForbidden = 60
	// rotatedFilterOperatorEmail supplies the operator email for the audited
	// access rollout commands.
	rotatedFilterOperatorEmail = "rollout-filter@example.com"
	// rotatedFilterBatches limits the number of raw ranker batches read from
	// one snapshot.
	rotatedFilterBatches = 10_000
)

// TestSearchRotatedVersionFiltersForbiddenPagesBeforeRanking checks raw
// ranking during candidate activation and after rollout completion. Both
// checks and public tack_search must include every allowed node and exclude
// every forbidden node.
func TestSearchRotatedVersionFiltersForbiddenPagesBeforeRanking(t *testing.T) {
	fixture := newRolloutFixture(t)
	caller, foreign := fixture.workspaces[0], otherOrganizationWorkspace(t, fixture)
	callerEntry, foreignEntry := rolloutEntryPoint(t, fixture, caller), rolloutEntryPoint(t, fixture, foreign)
	callerKind, foreignKind := putRolloutKind(t, fixture, caller.OrgID), putRolloutKind(t, fixture, foreign.OrgID)
	allowed := make([]uuid.UUID, 0, rotatedFilterAllowed)
	for number := range rotatedFilterAllowed {
		allowed = append(allowed, putRolloutNode(t, fixture, callerKind, callerEntry, fmt.Sprintf("Allowed %d", number), "notes that mention a saffron pipeline once"))
	}
	strong := strings.Repeat("Saffron pipeline saffron pipeline. ", 3)
	forbidden := make([]uuid.UUID, 0, rotatedFilterForbidden)
	for number := range rotatedFilterForbidden {
		forbidden = append(forbidden, putRolloutNode(t, fixture, foreignKind, foreignEntry, fmt.Sprintf("Saffron pipeline %d", number), strong))
	}
	drainRolloutWork(t, fixture)

	authorities := []uuid.UUID{caller.OrgID, foreign.OrgID}
	beginRotatedRollouts(t, fixture, authorities)
	for _, authority := range authorities {
		runRolloutUntil(t, fixture, authority, searchdomain.AccessRollout.Activated)
	}
	for _, page := range rolloutPages(t, fixture, forbidden) {
		if !slices.Contains(page.Access.Versions, searchaccess.RotatedVersion) {
			t.Fatalf("forbidden page %s stores versions %v without %s", page.ID, page.Access.Versions, searchaccess.RotatedVersion)
		}
	}
	requireRotatedRankerFilter(t, fixture, caller, callerEntry, allowed, forbidden)

	for _, authority := range authorities {
		final := runRolloutUntil(t, fixture, authority, rolloutStable)
		requireAccessRollout(t, final, searchaccess.RotatedVersion, []string{searchaccess.RotatedVersion}, searchdomain.AccessStable)
	}
	requirePageVersions(t, rolloutPages(t, fixture, append(slices.Clone(allowed), forbidden...)), []string{searchaccess.RotatedVersion})
	requireRotatedRankerFilter(t, fixture, caller, callerEntry, allowed, forbidden)
	found := traverseSearch(t, fixture, caller, rotatedFilterQuery, searchAs(t, fixture, caller, rotatedFilterQuery, ""))
	requireNodesFound(t, found, allowed)
	requireNodesAbsent(t, found, forbidden)
}

// beginRotatedRollouts runs each authority's rollout command through the
// audited command tree with the real SQL audit outbox. It requires backfill
// with the stable version active and both versions written.
func beginRotatedRollouts(t *testing.T, fixture rolloutFixture, authorities []uuid.UUID) {
	t.Helper()
	pool, err := pgxpool.New(rolloutContext(t), fixture.cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("cannot open outbox pool: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, authority := range authorities {
		output, err := runOpsCommand(t, fixture.cfg, pool,
			"--execute", "--operator-id", uuid.Must(uuid.NewV7()).String(), "--operator-email", rotatedFilterOperatorEmail,
			"ops", "search", "access-rollout", "--authority", authority.String(), "--candidate", searchaccess.RotatedVersion)
		if err != nil {
			t.Fatalf("access-rollout for authority %s failed: %v\n%s", authority, err, output)
		}
		requireAccessRollout(t, currentAccessRollout(t, fixture, authority), searchaccess.StableVersion, bothAccessVersions, searchdomain.AccessBackfill)
	}
}

// requireRotatedRankerFilter compiles caller keys under the active version,
// requires the candidate version, and checks raw ranker output.
func requireRotatedRankerFilter(t *testing.T, fixture rolloutFixture, caller datagen.WorkspaceIdentity, entryID uuid.UUID, allowed, forbidden []uuid.UUID) {
	t.Helper()
	members := membershipRepository(t, fixture)
	filter, denied := currentCallerAccess(t, fixture, members, caller.Actors[0].UserID, caller.OrgID, entryID)
	if denied || filter.Version != searchaccess.RotatedVersion {
		t.Fatalf("the caller's filter uses version %q with denied=%t instead of %s", filter.Version, denied, searchaccess.RotatedVersion)
	}
	ranked := rotatedRankedNodes(t, fixture, filter)
	requireNodesAbsent(t, ranked, forbidden)
	requireNodesFound(t, ranked, allowed)
}

// rotatedRankedNodes opens one snapshot through the production ranker and
// reads every raw batch in order.
func rotatedRankedNodes(t *testing.T, fixture rolloutFixture, filter searchdomain.AccessFilter) []uuid.UUID {
	t.Helper()
	ranker := fixture.adapter.Ranker(search.RankerSettings{KeepAlive: time.Minute, TokenBytes: 64 << 10, BatchSize: 100})
	query := searchdomain.Query{Text: rotatedFilterQuery, Index: fixture.index, NodeType: "", Access: filter, Generation: 0}
	snapshot, err := ranker.Open(rolloutContext(t), query)
	if err != nil {
		t.Fatalf("cannot open a snapshot: %v", err)
	}
	t.Cleanup(func() { _ = ranker.Close(rolloutContext(t), snapshot) })
	nodes := []uuid.UUID{}
	var after json.RawMessage
	for range rotatedFilterBatches {
		batch, err := ranker.Read(rolloutContext(t), query, snapshot, after)
		if err != nil {
			t.Fatalf("cannot read a ranked batch: %v", err)
		}
		if len(batch.Hits) == 0 {
			return nodes
		}
		snapshot.PITID = batch.PITID
		for _, hit := range batch.Hits {
			nodes = append(nodes, hit.NodeID)
		}
		after = batch.Hits[len(batch.Hits)-1].Sort
	}
	t.Fatalf("raw ranking did not end within %d batches", rotatedFilterBatches)
	return nil
}
