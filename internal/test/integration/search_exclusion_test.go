package integration

import (
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// recoveryOldText and recoveryNewText are the included values before and
	// after the edit, respectively.
	recoveryOldText = "quartz lantern harbor"
	recoveryNewText = "cobalt meadow signal"
	// recoveryReadFailure is the content read failure when decoding a
	// resolution record fails.
	recoveryReadFailure = "decode node resolution"
	// recoveryAttempts exceeds the attempt limit.
	recoveryAttempts int64 = 7
)

// TestSearchRebuildExcludesOrphanNodes runs a full FoundationDB replacement
// over two nodes without a hierarchy parent. Each copy fails access
// compilation. The replacement must still finish and move the public alias,
// and ops search verify must list both nodes. The test then deletes one
// orphan and adds a parent to the other. The delete must remove the first
// exclusion. The new parent must index the second node in the new serving
// index, make it searchable, and remove its exclusion.
func TestSearchRebuildExcludesOrphanNodes(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	steady := putOpaqueNode(t, fixture, kind, entry, "steady node", "amber orphan steady", readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 2000)
	orphan := putOrphanNode(t, kind, "orphan node", "amber orphan repaired")
	deleted := putOrphanNode(t, kind, "deleted orphan", "amber orphan deleted")

	request := searchdomain.BeginRebuild{Mode: searchdomain.ReplacementFull, PrimaryShards: 1, RoutingShards: 24, Replicas: 0, Restored: false, Reason: "test"}
	rebuild := beginRebuild(t, fixture, request)
	runRebuildUntil(t, fixture, rebuildFinished)
	requireServing(t, fixture, rebuild.TargetIndex, fixture.Index)
	drainSearchWork(t, fixture.Worker, 2000)
	requireCurrentPages(t, fixture, rebuild.TargetIndex, []uuid.UUID{steady})

	report, verifyErr := runSearchVerifyCommand(t, fixture.Config)
	t.Logf("ops search verify physical checks: %v", verifyErr)
	for _, nodeID := range []uuid.UUID{orphan, deleted} {
		reason, listed := report.reason(nodeID)
		if !listed || !strings.Contains(reason, "exactly one hierarchy parent") {
			t.Fatalf("ops search verify listed %+v, want node %s with its hierarchy failure", report, nodeID)
		}
		requireDeletedAbsent(t, fixture, rebuild.TargetIndex, nodeID)
	}
	for _, exclusion := range report.Exclusions {
		if exclusion.Class != string(searchdomain.WorkClassCopy) || exclusion.Index != rebuild.TargetIndex {
			t.Fatalf("exclusion of node %s names %s work on %s, want copy work on %s", exclusion.NodeID, exclusion.Class, exclusion.Index, rebuild.TargetIndex)
		}
	}
	if report.ExcludedNodes != len(report.Exclusions) || report.ExcludedNodes != 2 {
		t.Fatalf("ops search verify counted %d exclusions and listed %d, want 2", report.ExcludedNodes, len(report.Exclusions))
	}

	if err := fixture.Stores.Nodes.Delete(WithTestOperation(t, t.Context()), kind.OrgID, deleted); err != nil {
		t.Fatalf("delete orphan node: %v", err)
	}
	parent := &node.Relationship{OrgID: kind.OrgID, SourceID: orphan, TargetID: entry, RelationType: node.RelChildOf}
	if err := fixture.Stores.Relationships.Add(WithTestOperation(t, t.Context()), parent); err != nil {
		t.Fatalf("add parent of orphan node %s: %v", orphan, err)
	}
	drainSearchWork(t, fixture.Worker, 2000)

	requireCurrentPages(t, fixture, rebuild.TargetIndex, []uuid.UUID{orphan})
	results := callEverySearchPage(t, "amber orphan repaired", fixture.Harness)
	if !slices.Contains(results.IDs, orphan) {
		t.Fatalf("search after the repair returned %v, want node %s", results.IDs, orphan)
	}
	report, _ = runSearchVerifyCommand(t, fixture.Config)
	if report.ExcludedNodes != 0 {
		t.Fatalf("ops search verify still lists %+v after the repair and the delete", report.Exclusions)
	}
}

// TestSearchRecoveryRetainsWorkAfterFailedSourceReads indexes a node, edits
// it, and then replaces the node's resolution record with bytes that cannot
// decode as a resolution record. Every live slice of the node then fails a
// counted FoundationDB read. After seven failures, which exceeds the attempt
// limit, ops search verify must fail and list the live item with the failed
// read. Verify must not list the node as excluded. The live work must stay
// pending. The test then restores the resolution record without another
// edit. The pending work must index the edited text. Search must return the
// node. Verify must not list stuck work or an exclusion.
func TestSearchRecoveryRetainsWorkAfterFailedSourceReads(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	affected := putOpaqueNode(t, fixture, kind, entry, "recovery node", recoveryOldText, readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 2000)

	editOpaqueNode(t, fixture, kind, affected, recoveryNewText)
	resolveKey := nodeResolveRawKey(affected)
	resolve := replaceRawValue(t, resolveKey, []byte("{"))
	report, verifyErr := runUntilStuck(t, fixture, searchdomain.WorkClassLive, affected.String(), recoveryAttempts)
	for _, stuck := range report.StuckWork {
		if stuck.Kind == string(searchdomain.WorkClassLive) && stuck.ItemID == affected.String() && !strings.Contains(stuck.LastError, recoveryReadFailure) {
			t.Fatalf("stuck live work = %+v, want the failed read %q", stuck, recoveryReadFailure)
		}
	}
	if verifyErr == nil || !strings.Contains(verifyErr.Error(), "live work "+affected.String()) || !strings.Contains(verifyErr.Error(), recoveryReadFailure) {
		t.Fatalf("ops search verify error = %v, want live work %s and its failed read", verifyErr, affected)
	}
	if _, listed := report.reason(affected); listed {
		t.Fatalf("ops search verify lists node %s as excluded after %d failed reads", affected, recoveryAttempts)
	}
	requireLiveWorkPending(t, fixture, affected)

	replaceRawValue(t, resolveKey, resolve)
	deadline := clock.Now().Add(exclusionDeadline)
	for !indexedText(t, fixture, affected, recoveryNewText) {
		if clock.Now().After(deadline) {
			t.Fatalf("node %s did not index %q within %s after the read recovered", affected, recoveryNewText, exclusionDeadline)
		}
		runSliceOrPause(t, fixture.Worker)
	}
	drainSearchWork(t, fixture.Worker, 2000)
	requireCurrentPages(t, fixture, fixture.Index, []uuid.UUID{affected})
	if results := callEverySearchPage(t, recoveryNewText, fixture.Harness); !slices.Contains(results.IDs, affected) {
		t.Fatalf("search after the recovery returned %v, want node %s", results.IDs, affected)
	}
	report, _ = runSearchVerifyCommand(t, fixture.Config)
	if report.StuckWorkItems != 0 || report.ExcludedNodes != 0 {
		t.Fatalf("ops search verify after the recovery lists stuck work %+v and exclusions %+v", report.StuckWork, report.Exclusions)
	}
}
