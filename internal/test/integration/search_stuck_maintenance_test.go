package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// maintenanceAttemptLimit is the attempt count at which ops search verify
	// lists a work item, and at which the worker logs it.
	maintenanceAttemptLimit int64 = 5
	// maintenancePastLimit is one failure past the attempt limit.
	maintenancePastLimit = maintenanceAttemptLimit + 1
	// maintenanceLimitRecord is the record the worker logs when a counted
	// failure sets the attempt count of a work item to the limit.
	maintenanceLimitRecord = "search.work.attempt_limit_crossed"
	// malformedIssuedFailure is the cleanup read failure for an issued
	// document key without a document ID.
	malformedIssuedFailure = "issued-document identity is invalid"
	// malformedScanFailure is the scan failure for a node record key of the
	// organization with three tuple values.
	malformedScanFailure = "node record key has 3 tuple values"
)

// maintenanceItem is one cleanup, rescan, or rollout work item that fails a
// FoundationDB read with failure. NodeID is uuid.Nil for organization work.
type maintenanceItem struct {
	Class   searchdomain.WorkClass
	OrgID   uuid.UUID
	NodeID  uuid.UUID
	Failure string
}

// itemID returns the ID that ops search verify lists for the item.
func (item maintenanceItem) itemID() string {
	if item.NodeID == uuid.Nil {
		return item.OrgID.String()
	}
	return item.NodeID.String()
}

// TestSearchVerifyReportsStuckMaintenanceWork stores an issued document key
// without a document ID for an edited node and for a deleted node, and a
// node record key with three tuple values for their organization. The edit
// cleanup, the deletion cleanup, a containment rescan, and an access rollout
// then fail a counted FoundationDB read on every retry. Each item must keep
// retrying past the attempt limit. Ops search verify must fail and list each
// item with its kind, ID, attempt count, and last error. The worker must log
// the same record once for each item. The test then removes the malformed
// keys. Every item must complete and clear its attempt count.
func TestSearchVerifyReportsStuckMaintenanceWork(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entry := integration.EntryPoint(t, fixture, workspace)
	edited := integration.PutOpaqueNode(t, fixture, kind, entry, "stuck cleanup", "stuck cleanup before", "excluded")
	deleted := integration.PutOpaqueNode(t, fixture, kind, entry, "stuck deletion", "stuck deletion text", "excluded")
	integration.DrainSearchWork(t, fixture.Worker, 2000)

	malformed := []tuple.Tuple{
		{"search_issued", kind.OrgID.String(), edited.String(), int64(0), int64(0), "malformed"},
		{"search_issued", kind.OrgID.String(), deleted.String(), int64(0), int64(0), "malformed"},
	}
	for _, key := range malformed {
		setPrefixedValue(t, key, []byte{})
	}
	editDurableNode(t, fixture, kind, edited, "stuck cleanup after")
	if err := fixture.Stores.Nodes.Delete(integration.WithTestOperation(t, t.Context()), kind.OrgID, deleted); err != nil {
		t.Fatalf("delete node %s: %v", deleted, err)
	}
	requestContainmentRescan(t, fixture, kind)
	beginMaintenanceRollout(t, fixture, kind.OrgID)
	scanKey := tuple.Tuple{"node_instance", kind.OrgID.String(), ""}
	setPrefixedValue(t, scanKey, []byte("{}"))
	malformed = append(malformed, scanKey)

	items := []maintenanceItem{
		{Class: searchdomain.WorkClassCleanup, OrgID: kind.OrgID, NodeID: edited, Failure: malformedIssuedFailure},
		{Class: searchdomain.WorkClassCleanup, OrgID: kind.OrgID, NodeID: deleted, Failure: malformedIssuedFailure},
		{Class: searchdomain.WorkClassRescan, OrgID: kind.OrgID, NodeID: uuid.Nil, Failure: malformedScanFailure},
		{Class: searchdomain.WorkClassRollout, OrgID: kind.OrgID, NodeID: uuid.Nil, Failure: malformedScanFailure},
	}
	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	var report maintenanceReport
	var verifyErr error
	for _, item := range items {
		report, verifyErr = runUntilMaintenanceStuck(t, fixture, logger, item)
	}
	records := attemptLimitRecords(t, &logged)
	for _, item := range items {
		requireMaintenanceReported(t, report, verifyErr, records, item)
	}

	for _, key := range malformed {
		clearPrefixedKey(t, key)
	}
	runUntilMaintenanceClear(t, fixture, items)
	integration.DrainSearchWork(t, fixture.Worker, 2000)
	for _, document := range durableNodeDocuments(t, fixture, edited, false) {
		if document.Text == nil || !strings.Contains(*document.Text, "stuck cleanup after") {
			t.Fatalf("node %s keeps an active document without its edited text after the cleanup", edited)
		}
	}
	if active := durableNodeDocuments(t, fixture, deleted, false); len(active) != 0 {
		t.Fatalf("deleted node %s keeps %d active documents after the cleanup", deleted, len(active))
	}
	if report, _ := runMaintenanceVerify(t, fixture); report.StuckWorkItems != 0 {
		t.Fatalf("ops search verify lists %+v after every item completed", report.StuckWork)
	}
}

// requestContainmentRescan adds a type key to the CanContain list of kind,
// which requests a rescan of its organization.
func requestContainmentRescan(t *testing.T, fixture integration.QueryFixture, kind integration.OpaqueKind) {
	t.Helper()
	current, err := fixture.Stores.NodeTypes.TypeByKey(integration.WithTestOperation(t, t.Context()), kind.OrgID, kind.TypeKey)
	if err != nil || current == nil {
		t.Fatalf("read node type %s: %v", kind.TypeKey, err)
	}
	current.CanContain = append(current.CanContain, "c"+strings.ReplaceAll(uuid.NewString(), "-", ""))
	if err := fixture.Stores.NodeTypes.Set(integration.WithTestOperation(t, t.Context()), current); err != nil {
		t.Fatalf("change the containment of node type %s: %v", kind.TypeKey, err)
	}
}

// beginMaintenanceRollout starts an access rollout of authorityID to the
// rotated policy version through the production rollout store.
func beginMaintenanceRollout(t *testing.T, fixture integration.QueryFixture, authorityID uuid.UUID) {
	t.Helper()
	rollouts := fixture.Stores.SearchRollouts(clock.Wall{}, fixture.Stores.SearchPolicySet())
	current, err := rollouts.Current(integration.WithTestOperation(t, t.Context()), authorityID)
	if err != nil {
		t.Fatalf("read the access rollout of %s: %v", authorityID, err)
	}
	request := searchdomain.BeginAccessRollout{AuthorityID: authorityID, CandidateVersion: searchaccess.RotatedVersion, ExpectedGeneration: current.Generation}
	if _, err := rollouts.Begin(integration.WithTestOperation(t, t.Context()), request); err != nil {
		t.Fatalf("begin the access rollout of %s: %v", authorityID, err)
	}
}

// runUntilMaintenanceStuck runs worker slices with logger until ops search
// verify lists item with at least maintenancePastLimit attempts. It ignores
// slice failures and fails the test after maintenanceDeadline.
func runUntilMaintenanceStuck(t *testing.T, fixture integration.QueryFixture, logger *slog.Logger, item maintenanceItem) (maintenanceReport, error) {
	t.Helper()
	deadline := clock.Now().Add(maintenanceDeadline)
	for clock.Now().Before(deadline) {
		claimed, _ := fixture.Worker.RunSlice(telemetry.WithLogger(integration.WithTestOperation(t, t.Context()), logger))
		if claimed {
			continue
		}
		report, verifyErr := runMaintenanceVerify(t, fixture)
		if _, found := report.stuck(item); found {
			return report, verifyErr
		}
		waitUntilTime(t, clock.Now().Add(maintenancePoll))
	}
	t.Fatalf("ops search verify did not list %s work %s with %d attempts within %s", item.Class, item.itemID(), maintenancePastLimit, maintenanceDeadline)
	return maintenanceReport{}, nil
}

// requireMaintenanceReported requires the verify listing, the verify error,
// and exactly one attempt limit record to report item with its failure.
func requireMaintenanceReported(t *testing.T, report maintenanceReport, verifyErr error, records []attemptLimitRecord, item maintenanceItem) {
	t.Helper()
	stuck, found := report.stuck(item)
	if !found || !strings.Contains(stuck.LastError, item.Failure) {
		t.Fatalf("ops search verify listed %+v, want %s work %s with %d attempts and %q", report.StuckWork, item.Class, item.itemID(), maintenancePastLimit, item.Failure)
	}
	listed := fmt.Sprintf("%s work %s failed ", item.Class, item.itemID())
	if verifyErr == nil || !strings.Contains(verifyErr.Error(), listed) {
		t.Fatalf("ops search verify error = %v, want %q", verifyErr, listed)
	}
	matching := make([]attemptLimitRecord, 0, 1)
	for _, record := range records {
		if record.Class == string(item.Class) && record.ItemID == item.itemID() {
			matching = append(matching, record)
		}
	}
	if len(matching) != 1 || matching[0].Attempts != maintenanceAttemptLimit || matching[0].Err != stuck.LastError {
		t.Fatalf("attempt limit records of %s work %s = %+v, want one with %d attempts and %q", item.Class, item.itemID(), matching, maintenanceAttemptLimit, stuck.LastError)
	}
}

// attemptLimitRecords decodes every attempt limit record in logged.
func attemptLimitRecords(t *testing.T, logged *bytes.Buffer) []attemptLimitRecord {
	t.Helper()
	records := make([]attemptLimitRecord, 0, 4)
	decoder := json.NewDecoder(logged)
	for decoder.More() {
		var record attemptLimitRecord
		if err := decoder.Decode(&record); err != nil {
			t.Fatalf("decode a worker log record: %v", err)
		}
		if record.Message == maintenanceLimitRecord {
			records = append(records, record)
		}
	}
	return records
}
