package integration_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	rankingVersionNodes   = 40
	rankingVersionQuery   = "orbital manifest"
	rankingEarlierVersion = "1"
	rankingMismatchText   = "the search cursor does not match this request or is no longer current"
)

// TestSearchRankingVersionRejectsEarlierSessions verifies that the public
// tack_search tool rejects sessions from ranking version "1". It opens a
// public session and checks that the session replays its first page.
// It then stores a copy with a binding for ranking version "1" and requires
// the tool to refuse both a continuation and, after one committed page,
// a replay of the copy with the cursor mismatch error.
func TestSearchRankingVersionRejectsEarlierSessions(t *testing.T) {
	run := startRankingRun(t)
	created := make([]uuid.UUID, 0, rankingVersionNodes)
	for number := range rankingVersionNodes {
		created = append(created, run.createIssue(t, fmt.Sprintf("Orbital checklist %d", number), "orbital manifest checklist"))
	}
	run.waitIndexed(t, created)
	page := run.search(t, rankingVersionQuery, "")
	if page.Cursor == "" {
		t.Fatalf("the first page of %q completed the session", rankingVersionQuery)
	}
	current := run.session(t, page.Cursor)
	replayed := run.search(t, rankingVersionQuery, run.cursors.Encode(current.ID, 0))
	if !slices.Equal(replayed.IDs, page.IDs) {
		t.Fatalf("the current session replayed %v, want %v", replayed.IDs, page.IDs)
	}
	sessions := run.stores.SearchSessions(clock.Wall{}, 15*time.Minute)
	earlier := current
	earlier.ID = uuid.Must(uuid.NewV7())
	earlier.Binding = searchdomain.RankedSessionBinding(rankingEarlierVersion, current.PrincipalID, current.AuthorityID, current.EntryPointID, current.Query)
	earlier.Sort, earlier.Version, earlier.Complete, earlier.Closing = nil, 0, false, false
	if _, err := sessions.Create(integration.WithTestOperation(t, t.Context()), earlier); err != nil {
		t.Fatalf("create the earlier-version session: %v", err)
	}
	run.requireMismatch(t, "continuation", run.cursors.Encode(earlier.ID, 0))
	commit := searchdomain.PageCommit{Sort: current.Sort, PITID: current.Snapshot.PITID, Visited: page.IDs, ResultIDs: page.IDs, Complete: false}
	if _, err := sessions.CommitPage(integration.WithTestOperation(t, t.Context()), earlier.ID, 0, commit); err != nil {
		t.Fatalf("commit the first page of the earlier-version session: %v", err)
	}
	run.requireMismatch(t, "replay", run.cursors.Encode(earlier.ID, 0))
	run.completeCursor(t, rankingVersionQuery, page.Cursor)
}

func (r *rankingRun) requireMismatch(t *testing.T, label, cursor string) {
	t.Helper()
	_, err := r.driver.Call(integration.WithTestOperation(t, t.Context()), r.token, "tack_search", datagen.ToolArguments{
		WorkspaceReference: r.workspace, Query: rankingVersionQuery, Cursor: cursor,
	})
	if err == nil || !strings.Contains(err.Error(), rankingMismatchText) {
		t.Fatalf("%s of an earlier-version session returned %v, want %q", label, err, rankingMismatchText)
	}
	t.Logf("ranking version %s refused: %v", label, err)
}
