package integration_test

import (
	"encoding/base64"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
)

// cursorSessionIdle sets the idle timeout for the session inspection store.
// The store uses this timeout only when renewing a session. These tests only
// load sessions.
const cursorSessionIdle = 15 * time.Minute

// cursorSessionRecord decodes a tack_search cursor with the configured cursor
// key and loads its stored session from FoundationDB.
func cursorSessionRecord(t *testing.T, fixture rolloutFixture, cursor string) searchdomain.Session {
	t.Helper()
	key, err := base64.StdEncoding.DecodeString(os.Getenv("OPENSEARCH_CURSOR_KEY"))
	if err != nil {
		t.Fatalf("cannot decode cursor key: %v", err)
	}
	sessionID, _, err := tools.NewSearchCursorCodec(key).Decode(cursor)
	if err != nil {
		t.Fatalf("cannot decode cursor: %v", err)
	}
	session, err := fixture.stores.SearchSessions(clock.Wall{}, cursorSessionIdle).Load(rolloutContext(t), sessionID)
	if err != nil {
		t.Fatalf("cannot load session %s: %v", sessionID, err)
	}
	return session
}

// membershipRepository opens a pool on the fixture ledger and returns the
// production postgres.OrgMemberRepo. It closes the pool when the test ends.
func membershipRepository(t *testing.T, fixture rolloutFixture) *postgres.OrgMemberRepo {
	t.Helper()
	pool, err := pgxpool.New(rolloutContext(t), fixture.cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("cannot open membership pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return postgres.NewOrgMemberRepo(pool)
}

// currentCallerAccess uses the production policy set to compile and return
// the entry point's caller filter from current memberships and the
// authority's active access version. It returns true for
// searchaccess.ErrAccessDenied and fails the test on any other error.
func currentCallerAccess(t *testing.T, fixture rolloutFixture, members *postgres.OrgMemberRepo, principalID, authorityID, entryID uuid.UUID) (searchdomain.AccessFilter, bool) {
	t.Helper()
	organizations, err := members.ListOrgIDsForUser(rolloutContext(t), principalID)
	if err != nil {
		t.Fatalf("cannot list organizations of user %s: %v", principalID, err)
	}
	policies := fixture.stores.SearchPolicySet()
	version, err := fixture.stores.SearchRollouts(clock.Wall{}, policies).ActiveAccessVersion(rolloutContext(t), authorityID)
	if err != nil {
		t.Fatalf("cannot read the active access version of authority %s: %v", authorityID, err)
	}
	filter, err := policies.Query(rolloutContext(t), searchaccess.AccessRequest{
		Version: version, PrincipalID: principalID, AuthorityID: authorityID,
		EntryPointID: entryID, MemberOrganizations: organizations,
	})
	if errors.Is(err, searchaccess.ErrAccessDenied) {
		return searchdomain.AccessFilter{Version: "", Keys: nil}, true
	}
	if err != nil {
		t.Fatalf("cannot compile the access of user %s under entry point %s: %v", principalID, entryID, err)
	}
	return filter, false
}

// requireNoClaimableWork requires both a live work claim and an access work
// claim to return false and a nil error.
func requireNoClaimableWork(t *testing.T, fixture rolloutFixture) {
	t.Helper()
	store := fixture.stores.SearchWork(clock.Wall{})
	for _, class := range []searchdomain.WorkClass{searchdomain.WorkClassLive, searchdomain.WorkClassAccess} {
		work, claimed, err := store.Claim(rolloutContext(t), class, "membership-inspector", time.Minute)
		if err != nil || claimed {
			t.Fatalf("the membership change left %s work %+v with error %v", class, work, err)
		}
	}
}

// requireNodesAbsent requires that found does not contain any node in nodes.
func requireNodesAbsent(t *testing.T, found, nodes []uuid.UUID) {
	t.Helper()
	for _, nodeID := range nodes {
		if slices.Contains(found, nodeID) {
			t.Fatalf("search results %v include node %s", found, nodeID)
		}
	}
}
