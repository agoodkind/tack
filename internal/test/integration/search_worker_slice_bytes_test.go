package integration

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/service"
)

const (
	// escapedNameBytes exceeds the 512-byte name the page documents store.
	escapedNameBytes = 600
	// escapedValueBytes gives more than 32 full pages at the 4096-byte limit.
	escapedValueBytes = 110_000
	// worstEscapedText is one byte that JSON encoding writes as six bytes.
	worstEscapedText = "<"
	// largestAccessEntries is the version and key count of a page during an
	// access rollout: one key from each of the two registered policies.
	largestAccessEntries = 2
	// setupDeadline bounds the rollout and replacement setup steps.
	setupDeadline = 10 * time.Minute
)

// writeEscapedSearchNode rewrites the fixture node with a name and an
// included value made only of a character that JSON encoding escapes.
func writeEscapedSearchNode(t *testing.T, stores *fdbadapter.Stores, fixture searchFixture) {
	t.Helper()
	name := strings.Repeat(worstEscapedText, escapedNameBytes)
	props := map[string]json.RawMessage{
		fixture.IncludedKey: mustJSON(strings.Repeat(worstEscapedText, escapedValueBytes)),
		fixture.ExcludedKey: mustJSON(readerExcludedValue),
	}
	now := clock.Now().UTC()
	value := &node.Node{ID: fixture.NodeID, OrgID: fixture.OrgID, NodeType: fixture.TypeKey, Name: name, Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: fixture.NodeID, OrgID: fixture.OrgID, NodeType: fixture.TypeKey, Name: name, Props: props, CreatedAt: now, UpdatedAt: now}
	if err := stores.Nodes.Set(t.Context(), value, view); err != nil {
		t.Fatalf("store escaped search node: %v", err)
	}
}

// driveClasses claims and processes work of the listed classes only, until
// done reports true. Other classes stay pending.
func driveClasses(t *testing.T, worker *service.SearchWorker, work *fdbadapter.SearchWorkStore, classes []searchdomain.WorkClass, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(setupDeadline); time.Now().Before(deadline); {
		if done() {
			return
		}
		claimed := false
		for _, class := range classes {
			item, err := work.Claim(t.Context(), class, "setup", 30*time.Second)
			if errors.Is(err, searchdomain.ErrNoWork) {
				continue
			}
			if err != nil {
				t.Fatalf("claim %s work: %v", class, err)
			}
			claimed = true
			if err := worker.Process(t.Context(), item); err != nil {
				t.Fatalf("process %s work: %v", class, err)
			}
		}
		if !claimed {
			time.Sleep(250 * time.Millisecond)
		}
	}
	t.Fatalf("setup classes %v did not finish within %s", classes, setupDeadline)
}

// TestSearchWorkerLargestSliceStaysWithinByteMaximum measures the bulk bodies
// of one live slice under the production page and byte limits. The node has
// full pages of text that JSON encoding escapes six to one and a name above
// the stored name limit. An access rollout in its verifying phase gives each
// page two versions and two keys, and a replacement in its copying phase
// gives the work a mirror index. The slice must write 32 pages to the serving
// index and 32 to the mirror and send less than the 5 MiB byte maximum in
// total. The slice time and lease are longer than production; the page limit
// then stops the slice while the model encodes each page.
func TestSearchWorkerLargestSliceStaysWithinByteMaximum(t *testing.T) {
	production, err := config.LoadSearchWorkerSettings(t.Context())
	if err != nil {
		t.Fatalf("load production search worker settings: %v", err)
	}
	if production.PageBytes != 4096 || production.MaxPages != boundSlicePages || production.MaxBytes != 5<<20 {
		t.Fatalf("production settings = %d page bytes, %d pages, %d bytes; want 4096, %d, and %d", production.PageBytes, production.MaxPages, production.MaxBytes, boundSlicePages, 5<<20)
	}
	stores := newSearchStore(t)
	adapter, client, meter, serving := newMeteredSearchIndex(t, stores)
	source := clock.Wall{}
	fixture := putSearchText(t, stores, "escaped slice placeholder", readerExcludedValue)
	setup := newSearchWorker(t, stores, adapter, source, searchWorkerSettings(production.PageBytes))
	runSearchWorkerUntilIdle(t, setup)
	work := stores.SearchWork(source)

	rollouts := stores.SearchRollouts(source, stores.SearchPolicySet())
	current, err := rollouts.Current(t.Context(), fixture.OrgID)
	if err != nil {
		t.Fatalf("read access rollout: %v", err)
	}
	begin := searchdomain.BeginAccessRollout{AuthorityID: fixture.OrgID, CandidateVersion: searchaccess.RotatedVersion, ExpectedGeneration: current.Generation}
	if _, err := rollouts.Begin(t.Context(), begin); err != nil {
		t.Fatalf("begin access rollout: %v", err)
	}
	driveClasses(t, setup, work, []searchdomain.WorkClass{searchdomain.WorkClassRollout, searchdomain.WorkClassAccess}, func() bool {
		rollout, err := rollouts.Current(t.Context(), fixture.OrgID)
		if err != nil {
			t.Fatalf("read access rollout: %v", err)
		}
		if rollout.Phase != searchdomain.AccessBackfill && rollout.Phase != searchdomain.AccessVerifying {
			t.Fatalf("access rollout entered phase %s before the measurement", rollout.Phase)
		}
		return rollout.Phase == searchdomain.AccessVerifying
	})

	rebuilds := stores.SearchRebuilds(source)
	rebuild, err := rebuilds.BeginRebuild(t.Context(), searchdomain.BeginRebuild{
		Mode: searchdomain.ReplacementFull, PrimaryShards: 1, RoutingShards: 8, Replicas: 0, Restored: false, Reason: "largest slice measurement",
	})
	if err != nil {
		t.Fatalf("begin index replacement: %v", err)
	}
	t.Cleanup(func() { deleteNativeIndex(t, client, rebuild.TargetIndex) })
	driveClasses(t, setup, work, []searchdomain.WorkClass{searchdomain.WorkClassRebuild}, func() bool {
		replacement, found, err := rebuilds.CurrentRebuild(t.Context())
		if err != nil || !found {
			t.Fatalf("read index replacement: found %t err %v", found, err)
		}
		return replacement.State == searchdomain.RebuildCopying
	})

	writeEscapedSearchNode(t, stores, fixture)
	measured := boundSettings(production)
	item, err := work.Claim(t.Context(), searchdomain.WorkClassLive, "measured", measured.Lease)
	if err != nil || item.Target != serving || item.Mirror != rebuild.TargetIndex {
		t.Fatalf("claim live work = target %q mirror %q err %v, want %q and %q", item.Target, item.Mirror, err, serving, rebuild.TargetIndex)
	}
	meter.take()
	if err := newSearchWorker(t, stores, adapter, source, measured).Process(t.Context(), item); err != nil {
		t.Fatalf("process the measured live slice: %v", err)
	}
	requireLargestSliceBytes(t, meter.take(), serving, rebuild.TargetIndex, production)
	for _, index := range []string{serving, rebuild.TargetIndex} {
		page := searchNodePages(t, client, index, fixture.NodeID, false)[0]
		if len(page.Access.Versions) != largestAccessEntries || len(page.Access.Keys) != largestAccessEntries {
			t.Fatalf("page in %s has access %v, want %d versions and %d keys", index, page.Access, largestAccessEntries, largestAccessEntries)
		}
	}
}

// requireLargestSliceBytes requires 32 page writes to the serving index and
// 32 to the mirror index, with every bulk body totaling less than the byte
// maximum, and logs the measured numbers.
func requireLargestSliceBytes(t *testing.T, records []bulkRecord, serving, mirror string, production config.SearchWorkerSettings) {
	t.Helper()
	writes := map[string]int{}
	total, largest := 0, 0
	for _, record := range records {
		writes[record.index]++
		total += record.bytes
		largest = max(largest, record.bytes)
	}
	t.Logf("largest slice: %d serving and %d mirror page writes, %d encoded bytes in total, largest request %d bytes",
		writes[serving], writes[mirror], total, largest)
	if writes[serving] != production.MaxPages || writes[mirror] != production.MaxPages || len(writes) != 2 {
		t.Fatalf("slice wrote %v, want %d pages to each of %s and %s", writes, production.MaxPages, serving, mirror)
	}
	if total >= production.MaxBytes {
		t.Fatalf("slice sent %d encoded bytes, want less than the byte maximum %d", total, production.MaxBytes)
	}
}
