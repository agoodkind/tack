package integration_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	// recoveryStepPageBytes splits the edited text into several pages.
	recoveryStepPageBytes = 128
	// recoveryStepLease is the lease of each owner that the test stops.
	recoveryStepLease = 2 * time.Second
	// recoveryStepTimeout is the transaction timeout of reopened stores.
	recoveryStepTimeout = 5 * time.Second
	// recoveryStepBatch is the largest document batch of one cleanup slice.
	recoveryStepBatch = 100
	// recoveryStepMaxPages bounds the pages of the edited revision.
	recoveryStepMaxPages = 100
)

// TestSearchRecoveryResumesRefreshAndRetirement edits an indexed node and
// stops three owners in turn. The first owner checkpoints every page of the
// new revision and stops before the refresh. The second owner refreshes the
// index and stops before it completes the refresh. The third owner retires
// the documents of the old revision in OpenSearch and stops before its
// checkpoint. Each later claim comes from reopened stores after the previous
// lease expires. Both refresh claims must resume at the refresh step with
// the saved cursor, and the next cleanup claim must read the same pending
// documents. The worker must then finish both items. Only the pages of the
// new revision may stay active, and every page of the old revision must be
// retired.
func TestSearchRecoveryResumesRefreshAndRetirement(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entry := integration.EntryPoint(t, fixture, workspace)
	nodeID := integration.PutOpaqueNode(t, fixture, kind, entry, "recovery steps", strings.Repeat("first recovery text ", 20), "excluded")
	integration.DrainSearchWork(t, fixture.Worker, 2000)
	before := durableNodeDocuments(t, fixture, nodeID, false)
	editDurableNode(t, fixture, kind, nodeID, strings.Repeat("second recovery text ", 20))

	pagesOwner := claimRecoveryWork(t, reopenRecoveryStores(t), searchdomain.WorkClassLive, nodeID)
	pages, final := writeRecoveryPages(t, fixture, pagesOwner)
	waitUntilTime(t, pagesOwner.LeaseUntil)
	refreshOwner := claimRecoveryWork(t, reopenRecoveryStores(t), searchdomain.WorkClassLive, nodeID)
	requireRefreshResumed(t, refreshOwner, final)
	if err := fixture.Adapter.Refresh(integration.WithTestOperation(t, t.Context()), refreshOwner.Target); err != nil {
		t.Fatalf("refresh index %s: %v", refreshOwner.Target, err)
	}
	waitUntilTime(t, refreshOwner.LeaseUntil)
	resumed := claimRecoveryWork(t, reopenRecoveryStores(t), searchdomain.WorkClassLive, nodeID)
	requireRefreshResumed(t, resumed, final)
	if err := fixture.Worker.Process(integration.WithTestOperation(t, t.Context()), resumed); err != nil {
		t.Fatalf("process the resumed refresh of node %s: %v", nodeID, err)
	}

	retireOwner := claimRecoveryWork(t, reopenRecoveryStores(t), searchdomain.WorkClassCleanup, nodeID)
	pending := retireWithoutCheckpoint(t, fixture, retireOwner)
	waitUntilTime(t, retireOwner.LeaseUntil)
	store := reopenRecoveryStores(t).SearchWork(clock.Wall{})
	cleanup := claimRecoveryWork(t, reopenRecoveryStores(t), searchdomain.WorkClassCleanup, nodeID)
	again, err := store.Documents(integration.WithTestOperation(t, t.Context()), cleanup, recoveryStepBatch)
	if err != nil || !slices.Equal(recoveryDocumentIDs(again.Documents), recoveryDocumentIDs(pending)) {
		t.Fatalf("resumed cleanup documents = %v err %v, want the unconfirmed retirements %v", recoveryDocumentIDs(again.Documents), err, recoveryDocumentIDs(pending))
	}
	if err := fixture.Worker.Process(integration.WithTestOperation(t, t.Context()), cleanup); err != nil {
		t.Fatalf("process the resumed cleanup of node %s: %v", nodeID, err)
	}
	integration.DrainSearchWork(t, fixture.Worker, 2000)

	active := durableNodeDocuments(t, fixture, nodeID, false)
	if len(active) != len(pages) {
		t.Fatalf("node %s has %d active documents, want the %d pages of the new revision", nodeID, len(active), len(pages))
	}
	for position, document := range active {
		if document.Revision != pages[position].Revision || document.Text == nil || *document.Text != pages[position].Text {
			t.Fatalf("active document %d of node %s does not match page %d of revision %s", position, nodeID, position, pages[position].Revision)
		}
	}
	if retired := durableNodeDocuments(t, fixture, nodeID, true); len(retired) != len(before) {
		t.Fatalf("node %s has %d retired documents, want the %d pages of the old revision", nodeID, len(retired), len(before))
	}
	for _, class := range []searchdomain.WorkClass{searchdomain.WorkClassLive, searchdomain.WorkClassCleanup} {
		if work, err := store.Claim(integration.WithTestOperation(t, t.Context()), class, "inspector", recoveryStepLease); !errors.Is(err, searchdomain.ErrNoWork) {
			t.Fatalf("%s work %+v err %v remains after the recovery", class, work, err)
		}
	}
}

// reopenRecoveryStores opens new stores over the same cluster and test
// prefix, as a restarted process would.
func reopenRecoveryStores(t *testing.T) *fdbadapter.Stores {
	t.Helper()
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), recoveryStepTimeout, nil)
	if err != nil {
		t.Fatalf("reopen stores: %v", err)
	}
	stores.EnableSearchWork()
	return stores
}

// claimRecoveryWork claims the next item of class for recoveryStepLease and
// requires it to belong to nodeID.
func claimRecoveryWork(t *testing.T, stores *fdbadapter.Stores, class searchdomain.WorkClass, nodeID uuid.UUID) searchdomain.Work {
	t.Helper()
	work, err := stores.SearchWork(clock.Wall{}).Claim(integration.WithTestOperation(t, t.Context()), class, "owner-"+uuid.NewString(), recoveryStepLease)
	if err != nil || work.NodeID != nodeID {
		t.Fatalf("claim %s work = %+v err %v, want the work of node %s", class, work, err, nodeID)
	}
	return work
}

// writeRecoveryPages reads, registers, writes, and checkpoints every page of
// the claimed revision, and stops before the refresh. It returns the pages
// and the write of the final page.
func writeRecoveryPages(t *testing.T, fixture integration.QueryFixture, work searchdomain.Work) ([]node.ContentPage, searchdomain.WriteIntent) {
	t.Helper()
	store := fixture.Stores.SearchWork(clock.Wall{})
	reader := fixture.Stores.SearchContent(fixture.Stores.SearchPolicySet())
	request := searchdomain.ContentRequest{
		NodeID: work.NodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil,
		MaxBytes: recoveryStepPageBytes, SearchGeneration: work.Generation,
	}
	pages := make([]node.ContentPage, 0, recoveryStepMaxPages)
	for range recoveryStepMaxPages {
		page, err := reader.Content(integration.WithTestOperation(t, t.Context()), request)
		if err != nil {
			t.Fatalf("read page %d of node %s: %v", len(pages), work.NodeID, err)
		}
		intent, err := store.Register(integration.WithTestOperation(t, t.Context()), work, page)
		if err != nil {
			t.Fatalf("register page %d of node %s: %v", page.Ordinal, work.NodeID, err)
		}
		if _, err := fixture.Adapter.Put(integration.WithTestOperation(t, t.Context()), intent); err != nil {
			t.Fatalf("write page %d of node %s: %v", page.Ordinal, work.NodeID, err)
		}
		if err := store.CompletePage(integration.WithTestOperation(t, t.Context()), intent, page.NextCursor, page.Done); err != nil {
			t.Fatalf("checkpoint page %d of node %s: %v", page.Ordinal, work.NodeID, err)
		}
		pages = append(pages, page)
		if page.Done {
			return pages, intent
		}
		request.Cursor, work.Cursor, work.Ordinal = page.NextCursor, page.NextCursor, page.Ordinal+1
	}
	t.Fatalf("node %s did not finish within %d pages", work.NodeID, recoveryStepMaxPages)
	return nil, searchdomain.WriteIntent{}
}

// requireRefreshResumed requires work to resume at the refresh step after
// the checkpoint of final.
func requireRefreshResumed(t *testing.T, work searchdomain.Work, final searchdomain.WriteIntent) {
	t.Helper()
	if work.Phase != searchdomain.PhaseRefresh || work.Cursor != final.Page.NextCursor || work.Ordinal != final.Page.Ordinal+1 || work.Generation != final.Work.Generation {
		t.Fatalf("resumed work = %+v, want the refresh step after page %d of generation %d", work, final.Page.Ordinal, final.Work.Generation)
	}
}

// retireWithoutCheckpoint reads the obsolete documents of work and retires
// them in OpenSearch without the FoundationDB checkpoint. It returns the
// documents.
func retireWithoutCheckpoint(t *testing.T, fixture integration.QueryFixture, work searchdomain.Work) []searchdomain.IssuedDocument {
	t.Helper()
	batch, err := fixture.Stores.SearchWork(clock.Wall{}).Documents(integration.WithTestOperation(t, t.Context()), work, recoveryStepBatch)
	if err != nil || !batch.Done || len(batch.Documents) == 0 {
		t.Fatalf("obsolete documents of node %s = %+v err %v, want one complete batch", work.NodeID, batch, err)
	}
	intent := searchdomain.RetirementIntent{Work: work, Documents: batch.Documents}
	accepted, err := fixture.Adapter.Retire(integration.WithTestOperation(t, t.Context()), intent)
	if err != nil || accepted != len(batch.Documents) {
		t.Fatalf("retire %d documents of node %s: accepted %d err %v", len(batch.Documents), work.NodeID, accepted, err)
	}
	return batch.Documents
}

func recoveryDocumentIDs(documents []searchdomain.IssuedDocument) []string {
	ids := make([]string, 0, len(documents))
	for _, document := range documents {
		ids = append(ids, document.DocumentID)
	}
	return ids
}
