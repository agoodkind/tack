package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	legacyWriteHorizon = 25 * time.Second
	legacyDeadlineGap  = 5 * time.Second
)

type pendingUpgradeVariant uint8

const (
	pendingLegacyOnly pendingUpgradeVariant = iota
	pendingEarlierQualified
	pendingLaterQualified
	pendingOtherDocument
	pendingOtherIndex
)

// TestSearchWorkerAdoptsLegacyPendingWrite starts with a real abandoned request.
// It restores the deployed per-node record and checks adoption through Register,
// then requires the real worker to wait before resending the absent backend page.
func TestSearchWorkerAdoptsLegacyPendingWrite(t *testing.T) {
	cases := []struct {
		name    string
		variant pendingUpgradeVariant
	}{
		{name: "legacy_only", variant: pendingLegacyOnly},
		{name: "earlier_qualified_deadline", variant: pendingEarlierQualified},
		{name: "later_qualified_deadline", variant: pendingLaterQualified},
		{name: "different_document", variant: pendingOtherDocument},
		{name: "different_index", variant: pendingOtherIndex},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			requireLegacyPendingUpgrade(t, testCase.variant)
		})
	}
}

func requireLegacyPendingUpgrade(t *testing.T, variant pendingUpgradeVariant) {
	t.Helper()
	run := abandonPageOne(t, legacyWriteHorizon, 0)
	original := singleNodePending(t, run.env, run.nodeID)
	existing := qualifiedPendingFixture(t, run, original.record, variant)
	seedLegacyPending(t, run.env, run.nodeID, original, existing)
	expected := original.record
	if existing != nil && existing.DocumentID == expected.DocumentID && existing.Index == expected.Index && existing.Deadline.After(expected.Deadline) {
		expected = *existing
	}
	store := run.env.Stores.SearchWork(clock.Wall{})
	work := claimClassOf(t, store, searchdomain.WorkClassLive, run.nodeID)
	reader := run.env.Stores.SearchContent(run.env.Stores.SearchPolicySet())
	page, err := reader.Content(integration.WithTestOperation(t, t.Context()), searchdomain.ContentRequest{
		NodeID: work.NodeID, Cursor: work.Cursor, ProjectionConfig: work.Projection,
		AccessVersions: nil, MaxBytes: run.env.PageBytes, SearchGeneration: work.Generation,
	})
	if err != nil {
		t.Fatalf("read the claimed pending page of node %s: %v", run.nodeID, err)
	}
	intent, err := store.Register(integration.WithTestOperation(t, t.Context()), work, page)
	if err != nil {
		t.Fatalf("register the pending page of node %s: %v", run.nodeID, err)
	}
	if intent.Pending == nil || intent.Pending.DocumentID != expected.DocumentID || intent.Pending.Index != expected.Index ||
		!intent.Pending.Deadline.Equal(expected.Deadline) || !intent.Pending.AbandonedAt.Equal(expected.AbandonedAt) {
		t.Fatalf("adopted pending write = %+v, want document %s in %s with original deadline %s", intent.Pending, expected.DocumentID, expected.Index, expected.Deadline)
	}
	qualifiedKey := pendingFixtureKey(t, work.Class, work.OrgID, work.NodeID, expected.DocumentID, expected.Index)
	requirePendingFixture(t, qualifiedKey, expected)
	if existing != nil && (existing.DocumentID != expected.DocumentID || existing.Index != expected.Index) {
		requirePendingFixture(t, pendingFixtureKey(t, work.Class, work.OrgID, work.NodeID, existing.DocumentID, existing.Index), *existing)
	}
	requireLegacyPendingRemoved(t, run.env, run.nodeID)
	if err := store.Yield(integration.WithTestOperation(t, t.Context()), work); err != nil {
		t.Fatalf("yield the inspected pending claim of node %s: %v", run.nodeID, err)
	}
	bound := expected.Deadline.Add(mirrorFreshnessBound)
	if !runSlicesUntil(t, run.env, bound, func() bool { return indexMismatch(t, run.env, run.nodeID) == "" }) {
		t.Fatalf("node %s remains incomplete after its adopted deadline: %s", run.nodeID, indexMismatch(t, run.env, run.nodeID))
	}
	writes := pageWrites(run.proxy, run.nodeID, expected.Ordinal)
	if len(writes) < 2 || writes[1].received.Before(expected.Deadline.Add(-abandonClockSkew)) || writes[1].revision != page.Revision || writes[1].index != expected.Index {
		t.Fatalf("pending document %s writes = %+v, want its resend at or after %s in %s", expected.DocumentID, writes, expected.Deadline, expected.Index)
	}
}

func qualifiedPendingFixture(t *testing.T, run abandonedPage, original pendingFixtureRecord, variant pendingUpgradeVariant) *pendingFixtureRecord {
	t.Helper()
	existing := original
	switch variant {
	case pendingLegacyOnly:
		return nil
	case pendingEarlierQualified:
		existing.Deadline = existing.Deadline.Add(-legacyDeadlineGap)
	case pendingLaterQualified:
		existing.Deadline = existing.Deadline.Add(legacyDeadlineGap)
	case pendingOtherDocument:
		page := run.env.readerPages(t, run.nodeID)[0]
		existing.DocumentID = searchdomain.DocumentID(run.env.OrgID, run.nodeID, page.Revision, page.ProjectionVersion, page.Ordinal)
		existing.Ordinal = page.Ordinal
	case pendingOtherIndex:
		existing.Index = createPendingFixtureIndex(t, run.env)
	}
	return &existing
}

func createPendingFixtureIndex(t *testing.T, env *migrationEnv) string {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	model, err := env.Adapter.Provision(ctx)
	if err != nil {
		t.Fatalf("provision the second pending-write index model: %v", err)
	}
	index := "node-pages-" + uuid.Must(uuid.NewV7()).String()
	spec := search.IndexSpec{Model: model, MappingVersion: search.MappingVersion, Primaries: 1, RoutingShards: 8, Replicas: 0}
	if err := env.Adapter.EnsureIndex(ctx, index, spec); err != nil {
		t.Fatalf("create the second pending-write index %s: %v", index, err)
	}
	t.Cleanup(func() {
		if err := env.Adapter.DeleteIndex(context.WithoutCancel(ctx), index); err != nil {
			t.Errorf("delete the second pending-write index %s: %v", index, err)
		}
	})
	return index
}
