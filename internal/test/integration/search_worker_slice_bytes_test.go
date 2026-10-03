package integration

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
)

// TestSearchWorkerLargestSliceStaysWithinByteMaximum measures live slices
// under the production page and byte limits while an access rollout in its
// verifying phase gives each page two versions and two keys and a
// replacement in its copying phase gives the work a mirror index. The slice
// time and lease are longer than production; the page limit, not the clock,
// ends a slice.
//
// The store writes the whole node, every property included, as one JSON
// value at one key (internal/adapters/foundationdb/node_update.go:88-94), and
// the content reader reads every property from that value
// (node_content_snapshot.go:67-79). FoundationDB refuses a value above
// 100,000 bytes (error 2103). 32 pages at 4096 bytes with a 1024-byte overlap
// need at least 4096 + 31 x 3072 = 99,328 projected bytes for a full final
// page; text that JSON encodes six to one needs about 596,000 stored bytes.
// No persisted node has 32 full pages of that text.
//
// The test measures two nodes of the largest stored size. The plain-text
// node has at least 32 pages; its slice must write 32 pages to each index and
// send less than 5 MiB. The escaped node has a 512-character escaped name and
// escaped text; its largest page request, times 32 pages, times two indexes,
// is the upper bound for a production slice and must stay below 5 MiB.
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
	plain := putSearchText(t, stores, "plain slice placeholder", readerExcludedValue)
	escaped := putSearchTextInOrg(t, stores, plain.OrgID, "escaped slice placeholder", readerExcludedValue)
	setup := newSearchWorker(t, stores, adapter, source, searchWorkerSettings(production.PageBytes))
	runSearchWorkerUntilIdle(t, setup)
	work := stores.SearchWork(source)

	rollouts := stores.SearchRollouts(source, stores.SearchPolicySet())
	current, err := rollouts.Current(t.Context(), plain.OrgID)
	if err != nil {
		t.Fatalf("read access rollout: %v", err)
	}
	begin := searchdomain.BeginAccessRollout{AuthorityID: plain.OrgID, CandidateVersion: searchaccess.RotatedVersion, ExpectedGeneration: current.Generation}
	if _, err := rollouts.Begin(t.Context(), begin); err != nil {
		t.Fatalf("begin access rollout: %v", err)
	}
	driveClasses(t, setup, work, []searchdomain.WorkClass{searchdomain.WorkClassRollout, searchdomain.WorkClassAccess}, func() bool {
		rollout, err := rollouts.Current(t.Context(), plain.OrgID)
		if err != nil {
			t.Fatalf("read access rollout: %v", err)
		}
		if rollout.Phase != searchdomain.AccessBackfill && rollout.Phase != searchdomain.AccessVerifying {
			t.Fatalf("access rollout entered phase %s before the measurement", rollout.Phase)
		}
		return rollout.Phase == searchdomain.AccessVerifying
	})
	// The final scan page schedules access work in the transaction that
	// enters the verifying phase (search_access_rollout_steps.go:21-35). That
	// work records both write versions on each node before the measurement.
	drainClass(t, work, setup, searchdomain.WorkClassAccess)

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

	writeLargestSearchNode(t, stores, plain, "plain", "a")
	writeLargestSearchNode(t, stores, escaped, strings.Repeat(worstEscapedText, escapedNameBytes), worstEscapedText)
	plainPages := len(readSearchPages(t, stores, plain.NodeID, production.PageBytes))
	if plainPages < production.MaxPages {
		t.Fatalf("the largest plain node has %d pages, want at least %d", plainPages, production.MaxPages)
	}
	measured := boundSettings(production)
	slices := measureLiveSlices(t, work, newSearchWorker(t, stores, adapter, source, measured), meter, measured.Lease, serving, rebuild.TargetIndex)

	writes, total, largest := sliceBytes(slices[plain.NodeID])
	t.Logf("plain node slice: %d serving and %d mirror page writes, %d encoded bytes, largest request %d bytes", writes[serving], writes[rebuild.TargetIndex], total, largest)
	if writes[serving] != production.MaxPages || writes[rebuild.TargetIndex] != production.MaxPages || len(writes) != 2 {
		t.Fatalf("plain node slice wrote %v, want %d pages to each of %s and %s", writes, production.MaxPages, serving, rebuild.TargetIndex)
	}
	for _, record := range slices[plain.NodeID] {
		if record.ordinal < uint64(plainPages-1) && record.textBytes != production.PageBytes {
			t.Fatalf("plain node page %d in %s has %d text bytes, want the full page %d", record.ordinal, record.index, record.textBytes, production.PageBytes)
		}
	}
	if total >= production.MaxBytes {
		t.Fatalf("plain node slice sent %d encoded bytes, want less than %d", total, production.MaxBytes)
	}
	writes, total, largest = sliceBytes(slices[escaped.NodeID])
	bound := 2 * production.MaxPages * largest
	t.Logf("escaped node slice: %d serving and %d mirror page writes, %d encoded bytes, largest request %d bytes; production slice upper bound 2 x %d x %d = %d bytes", writes[serving], writes[rebuild.TargetIndex], total, largest, production.MaxPages, largest, bound)
	if writes[serving] == 0 || writes[serving] != writes[rebuild.TargetIndex] || len(writes) != 2 {
		t.Fatalf("escaped node slice wrote %v, want equal page writes to %s and %s", writes, serving, rebuild.TargetIndex)
	}
	if bound >= production.MaxBytes {
		t.Fatalf("32 pages of the largest escaped request on two indexes = %d bytes, want less than %d", bound, production.MaxBytes)
	}
	measuredRollout, err := rollouts.Current(t.Context(), plain.OrgID)
	if err != nil {
		t.Fatalf("read access rollout: %v", err)
	}
	t.Logf("rollout at measurement: phase %s generation %d write versions %v", measuredRollout.Phase, measuredRollout.Generation, measuredRollout.WriteVersions)
	for _, nodeID := range []uuid.UUID{plain.NodeID, escaped.NodeID} {
		for _, index := range []string{serving, rebuild.TargetIndex} {
			for _, stored := range searchNodePages(t, client, index, nodeID, false) {
				t.Logf("page %s ordinal %d in %s: generation %s access versions %v, %d keys, access generation %d", nodeID, stored.PageOrdinal, index, stored.SearchGeneration, stored.Access.Versions, len(stored.Access.Keys), stored.Access.Generation)
			}
			page := searchNodePages(t, client, index, nodeID, false)[0]
			if len(page.Access.Versions) != largestAccessEntries || len(page.Access.Keys) != largestAccessEntries {
				t.Fatalf("page of %s in %s has access %v, want %d versions and %d keys", nodeID, index, page.Access, largestAccessEntries, largestAccessEntries)
			}
		}
	}
}
