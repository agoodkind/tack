//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	appruntime "goodkind.io/tack/internal/runtime"
)

// retiredAuditQuery is the included text of the retirement corpus.
const retiredAuditQuery = "ashen weir retired audit"

type retirementExtra struct {
	RebuildID        string    `json:"rebuild_id"`
	SourceIndex      string    `json:"source_index"`
	InitiatedActorID uuid.UUID `json:"initiated_actor_id"`
}

// TestSearchRuntimeAuditRetiredSessionEvents opens a session on the serving
// index and runs ops search reindex until the worker retires the source index.
// The session update and delete must use the tack-search-worker actor and
// state the reindex operation as parent. Their Extra must store the rebuild
// ID, the source index, and the initiating actor.
func TestSearchRuntimeAuditRetiredSessionEvents(t *testing.T) {
	setShortSessions(t)
	fixture := newSessionAuditFixture(t)
	fixture.putCorpus(t, retiredAuditQuery, sessionAuditNodes)
	owner := fixture.first.Actors[0]
	page := fixture.mustSearch(t, owner.Token, retiredAuditQuery, "")
	session := sessionOfCursor(t, fixture.parentEdgeFixture, page.Cursor)
	source := servingIndex(t, fixture.parentEdgeFixture)
	beginReplacement(t, fixture.parentEdgeFixture)
	rebuild, found, err := fixture.stores.SearchRebuilds(clock.Wall{}).CurrentRebuild(t.Context())
	if err != nil || !found {
		t.Fatalf("read the index replacement: found %t, %v", found, err)
	}
	runReplacementUntil(t, fixture.parentEdgeFixture, replacementFinished)

	rows := fixture.waitForRows(t, fixture.first.OrgID, func(current []audit.Row) bool {
		return len(sessionChanges(current, session.ID, audit.VerbSearchSessionDelete, audit.TargetSearchSession)) == 1 &&
			len(closingUpdates(t, current, session.ID)) == 1
	})
	workerID := cli.ServiceActorID(appruntime.SearchWorkerService)
	deleted := onlyRow(t, sessionChanges(rows, session.ID, audit.VerbSearchSessionDelete, audit.TargetSearchSession), "the session delete")
	for _, row := range []audit.Row{closingUpdates(t, rows, session.ID)[0], deleted} {
		requireRetirementRow(t, row, workerID, rebuild, source)
	}
}

func requireRetirementRow(t *testing.T, row audit.Row, workerID uuid.UUID, rebuild searchdomain.Rebuild, source string) {
	t.Helper()
	if row.ActorID != workerID || row.Context.ParentOperationID != rebuild.Initiation.OperationID {
		t.Fatalf("row %s has actor %s and parent %s, want actor %s and parent %s",
			row.EventID, row.ActorID, row.Context.ParentOperationID, workerID, rebuild.Initiation.OperationID)
	}
	var extra retirementExtra
	if err := json.Unmarshal(row.Extra, &extra); err != nil {
		t.Fatalf("decode row %s evidence %s: %v", row.EventID, row.Extra, err)
	}
	if extra.RebuildID != rebuild.ID.String() || extra.SourceIndex != source || extra.InitiatedActorID != rebuild.Initiation.ActorID {
		t.Fatalf("row %s evidence = %+v, want rebuild %s, source %s, and actor %s", row.EventID, extra, rebuild.ID, source, rebuild.Initiation.ActorID)
	}
}
