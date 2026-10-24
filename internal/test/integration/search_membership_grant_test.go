package integration_test

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/org"
)

const (
	// membershipGrantQuery is the query text that every corpus node in both
	// organizations contains.
	membershipGrantQuery = "membership grant harbor"
	// membershipGrantNodes sets the corpus size to 30 nodes per organization,
	// exceeding the page size of 25 results.
	membershipGrantNodes = 30
	// membershipGrantRole sets the added membership's role to member, with
	// value 15.
	membershipGrantRole = 15
)

// TestSearchMembershipGrantUsesNewCallerKeys verifies that a membership grant
// changes new session keys without writing indexed pages. Earlier sessions
// retain their snapshots and check current node access during pagination.
func TestSearchMembershipGrantUsesNewCallerKeys(t *testing.T) {
	fixture := newRolloutFixture(t)
	home, homeSecond := sameOrganizationWorkspaces(t, fixture)
	foreign := otherOrganizationWorkspace(t, fixture)
	if foreign.OrgID == home.OrgID {
		t.Fatalf("workspace %s and workspace %s share organization %s", home.Slug, foreign.Slug, home.OrgID)
	}
	callerID := home.Actors[0].UserID
	homeEntry, foreignEntry := rolloutEntryPoint(t, fixture, home), rolloutEntryPoint(t, fixture, foreign)
	homeKind := putRolloutKind(t, fixture, home.OrgID)
	homeNodes := putRolloutCorpus(t, fixture, homeKind, homeEntry, membershipGrantQuery, membershipGrantNodes)
	foreignNodes := putRolloutCorpus(t, fixture, putRolloutKind(t, fixture, foreign.OrgID), foreignEntry, membershipGrantQuery, membershipGrantNodes)
	drainRolloutWork(t, fixture)
	members := membershipRepository(t, fixture)
	if _, denied := currentCallerAccess(t, fixture, members, callerID, foreign.OrgID, foreignEntry); !denied {
		t.Fatalf("user %s can search organization %s before the grant", callerID, foreign.OrgID)
	}
	established := searchAs(t, fixture, home, membershipGrantQuery, "")
	if established.complete {
		t.Fatalf("search for %q completed in one page", membershipGrantQuery)
	}
	session := cursorSessionRecord(t, fixture, established.cursor)
	corpus := append(slices.Clone(homeNodes), foreignNodes...)
	unwritten := indexedPages(t, fixture, corpus)

	grant := &org.Member{ID: uuid.Nil, OrgID: foreign.OrgID, UserID: callerID, Role: membershipGrantRole, CreatedAt: time.Time{}}
	if err := members.AddMember(rolloutContext(t), grant); err != nil {
		t.Fatalf("cannot add user %s to organization %s: %v", callerID, foreign.OrgID, err)
	}
	requireNoClaimableWork(t, fixture)
	drainRolloutWork(t, fixture)
	requireIndexedPagesUnwritten(t, unwritten, indexedPages(t, fixture, corpus))

	granted, denied := currentCallerAccess(t, fixture, members, callerID, foreign.OrgID, foreignEntry)
	if denied || slices.Equal(granted.Keys, session.Query.Access.Keys) {
		t.Fatalf("the grant must give user %s new caller keys: denied=%t keys=%v", callerID, denied, granted.Keys)
	}
	asCaller := foreign
	asCaller.Actors = home.Actors
	opened := searchAs(t, fixture, asCaller, membershipGrantQuery, "")
	if opened.complete {
		t.Fatalf("search under organization %s completed in one page", foreign.OrgID)
	}
	if keys := cursorSessionRecord(t, fixture, opened.cursor).Query.Access.Keys; !slices.Equal(keys, granted.Keys) {
		t.Fatalf("the new session stores caller keys %v instead of %v", keys, granted.Keys)
	}
	foreignFound := traverseSearch(t, fixture, asCaller, membershipGrantQuery, opened)
	requireNodesFound(t, foreignFound, foreignNodes)
	requireNodesAbsent(t, foreignFound, homeNodes)

	kept := cursorSessionRecord(t, fixture, established.cursor).Snapshot
	if kept.PITID != session.Snapshot.PITID || kept.Index != session.Snapshot.Index || !bytes.Equal(kept.QueryTokens, session.Snapshot.QueryTokens) {
		t.Fatalf("the established session changed its snapshot from %+v to %+v", session.Snapshot, kept)
	}
	remaining := slices.DeleteFunc(slices.Clone(homeNodes), func(nodeID uuid.UUID) bool {
		return slices.Contains(established.ids, nodeID)
	})
	moved := remaining[0]
	moveRolloutNode(t, fixture, moved, rolloutEntryPoint(t, fixture, homeSecond))
	later := putRolloutNode(t, fixture, homeKind, homeEntry, "Membership grant later node", membershipGrantQuery)
	drainRolloutWork(t, fixture)
	continued := traverseSearch(t, fixture, home, membershipGrantQuery, established)
	requireNodesFound(t, continued, slices.DeleteFunc(slices.Clone(homeNodes), func(nodeID uuid.UUID) bool { return nodeID == moved }))
	requireNodesAbsent(t, continued, append(slices.Clone(foreignNodes), moved, later))
	requireNodesFound(t, traverseSearch(t, fixture, home, membershipGrantQuery, searchAs(t, fixture, home, membershipGrantQuery, "")), []uuid.UUID{later})
}
