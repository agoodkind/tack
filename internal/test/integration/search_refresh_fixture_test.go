package integration

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
)

// rolloutDeadline bounds one complete access rollout in a test.
const rolloutDeadline = 5 * time.Minute

// rolloutAbandonLease is the lease of a rollout claim a stopped process
// leaves behind. The next worker claims the step after it expires.
const rolloutAbandonLease = 500 * time.Millisecond

// putUndeclaredNode creates a node of kind under parentID with one value
// under a property name that has no definition yet.
func putUndeclaredNode(t *testing.T, fixture queryFixture, kind opaqueKind, parentID uuid.UUID, name, value string) uuid.UUID {
	t.Helper()
	nodeID := uuid.Must(uuid.NewV7())
	props := map[string]json.RawMessage{name: mustJSON(value)}
	now := clock.Now().UTC()
	created := &node.Node{ID: nodeID, OrgID: kind.OrgID, NodeType: kind.TypeKey, Name: "undeclared node", Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: nodeID, OrgID: kind.OrgID, NodeType: kind.TypeKey, Name: "undeclared node", Props: props, CreatedAt: now, UpdatedAt: now}
	parent := &node.Relationship{OrgID: kind.OrgID, SourceID: nodeID, TargetID: parentID, RelationType: node.RelChildOf}
	if err := fixture.Stores.Nodes.CreateAtomic(t.Context(), created, view, []*node.Relationship{parent}, nil, nil, nil); err != nil {
		t.Fatalf("create undeclared node: %v", err)
	}
	return nodeID
}

// runSlicesIgnoringFailures runs up to limit slices and ignores slice
// failures. Tests use it for work that fails until a later metadata write.
func runSlicesIgnoringFailures(t *testing.T, worker *service.SearchWorker, limit int) {
	t.Helper()
	for range limit {
		claimed, err := worker.RunSlice(t.Context())
		if err == nil && !claimed {
			return
		}
	}
}

// requireAliasTarget requires the public alias to still name the fixture's
// physical index.
func requireAliasTarget(t *testing.T, fixture queryFixture) {
	t.Helper()
	target, err := fixture.Adapter.AliasTarget(t.Context(), search.PublicAlias)
	if err != nil || target != fixture.Index {
		t.Fatalf("public alias target = %q, error = %v, want %q", target, err, fixture.Index)
	}
}

// pagesOf returns the active pages of every node in order.
func pagesOf(t *testing.T, fixture queryFixture, nodes []uuid.UUID) []searchPageSource {
	t.Helper()
	pages := make([]searchPageSource, 0, len(nodes)*2)
	for _, nodeID := range nodes {
		pages = append(pages, searchNodePages(t, fixture.Client, fixture.Index, nodeID, false)...)
	}
	if len(pages) == 0 {
		t.Fatal("the nodes have no active pages")
	}
	return pages
}

// requireSemanticPreserved requires the same documents with byte-identical
// text and semantic fields.
func requireSemanticPreserved(t *testing.T, before, after []searchPageSource) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("active pages changed from %d to %d", len(before), len(after))
	}
	for position, page := range after {
		previous := before[position]
		if page.ID != previous.ID || page.NodeRevision != previous.NodeRevision || !bytes.Equal(page.Semantic, previous.Semantic) ||
			page.PageText == nil || previous.PageText == nil || *page.PageText != *previous.PageText {
			t.Fatalf("page %d changed its identity, text, or semantic fields", position)
		}
	}
}

// requireAccessVersions requires every page to store exactly the access
// versions in versions.
func requireAccessVersions(t *testing.T, pages []searchPageSource, versions []string) {
	t.Helper()
	for position, page := range pages {
		if !slices.Equal(page.Access.Versions, versions) {
			t.Fatalf("page %d access versions = %v, want %v", position, page.Access.Versions, versions)
		}
	}
}

// sameOrgWorkspaces returns two bootstrapped workspaces of one organization.
func sameOrgWorkspaces(t *testing.T, fixture queryFixture) (datagen.WorkspaceIdentity, datagen.WorkspaceIdentity) {
	t.Helper()
	for first, workspace := range fixture.Workspaces {
		for _, other := range fixture.Workspaces[first+1:] {
			if other.OrgID == workspace.OrgID {
				return workspace, other
			}
		}
	}
	t.Fatal("no organization has two workspaces")
	return datagen.WorkspaceIdentity{}, datagen.WorkspaceIdentity{}
}

// beginRollout uses the production rollout store to start an access rollout
// for authorityID with candidate version candidate.
func beginRollout(t *testing.T, fixture queryFixture, authorityID uuid.UUID, candidate string) {
	t.Helper()
	rollouts := fixture.Stores.SearchRollouts(clock.Wall{}, fixture.Stores.SearchPolicySet())
	current, err := rollouts.Current(t.Context(), authorityID)
	if err != nil {
		t.Fatalf("read access rollout: %v", err)
	}
	request := searchdomain.BeginAccessRollout{AuthorityID: authorityID, CandidateVersion: candidate, ExpectedGeneration: current.Generation}
	if _, err := rollouts.Begin(t.Context(), request); err != nil {
		t.Fatalf("begin access rollout: %v", err)
	}
}

// runRolloutUntilStable runs worker slices until the authority's rollout
// returns to the stable phase. onPhase runs once for each phase it sees.
func runRolloutUntilStable(t *testing.T, fixture queryFixture, worker *service.SearchWorker, authorityID uuid.UUID, onPhase func(searchdomain.AccessPhase)) searchdomain.AccessRollout {
	t.Helper()
	rollouts := fixture.Stores.SearchRollouts(clock.Wall{}, fixture.Stores.SearchPolicySet())
	seen := make(map[searchdomain.AccessPhase]bool)
	deadline := time.Now().Add(rolloutDeadline)
	for time.Now().Before(deadline) {
		current, err := rollouts.Current(t.Context(), authorityID)
		if err != nil {
			t.Fatalf("read access rollout: %v", err)
		}
		if current.Phase == searchdomain.AccessStable {
			return current
		}
		if !seen[current.Phase] {
			seen[current.Phase] = true
			onPhase(current.Phase)
		}
		runSliceOrPause(t, worker)
	}
	t.Fatalf("access rollout of %s did not finish within %s", authorityID, rolloutDeadline)
	return searchdomain.AccessRollout{}
}

// idleSlicePause is the delay after a worker slice that claimed no work.
const idleSlicePause = 250 * time.Millisecond

// runSliceOrPause runs one worker slice and fails the test when the slice
// returns an error. It waits idleSlicePause when the worker claimed no work.
func runSliceOrPause(t *testing.T, worker *service.SearchWorker) {
	t.Helper()
	claimed, err := worker.RunSlice(t.Context())
	if err != nil {
		t.Fatalf("run search worker slice: %v", err)
	}
	if !claimed {
		time.Sleep(idleSlicePause)
	}
}
