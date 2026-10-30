package integration_test

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// TestSearchOperatorReadsFinalizeBeforeOutput executes both public reads and
// checks their permanent ledger events against the exact printed payloads.
func TestSearchOperatorReadsFinalizeBeforeOutput(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	integration.ConfigureSearchMaintenance(t, testenv.OpenSearch(t))
	t.Setenv("OPENSEARCH_SHARDS", "1")
	t.Setenv("OPENSEARCH_ROUTING_SHARDS", "24")
	t.Setenv("OPENSEARCH_REPLICAS", "0")
	wantDiagnosticURIs := createSearchReadDiagnostics(t, fixture)
	pool := ledgerAdminPool(t)
	var events []audit.Event
	returnedTargets := make(map[uuid.UUID][]audit.Target)
	for _, current := range []struct {
		command   string
		verb      audit.Verb
		reference string
	}{
		{command: "settings", verb: audit.VerbOpsSearchSettings, reference: "opensearch/settings"},
		{command: "verify", verb: audit.VerbOpsSearchVerify, reference: "opensearch/verification"},
	} {
		operatorID := uuid.Must(uuid.NewV7())
		root, output := searchReadCommand(t, fixture.Config, pool, operatorID, "ops", "search", current.command)
		if err := root.ExecuteContext(t.Context()); err != nil {
			t.Fatalf("ops search %s failed: %v\n%s", current.command, err, output.String())
		}
		payload := printedPayload(t, output.Bytes())
		targets := []audit.Target{externalReference(current.reference)}
		if current.command == "verify" {
			targets = searchVerifyPayloadTargets(t, payload)
			for _, uri := range wantDiagnosticURIs {
				if !slices.ContainsFunc(targets, func(target audit.Target) bool { return target.URI == uri }) {
					t.Fatalf("verify omitted diagnostic %s from %+v", uri, targets)
				}
			}
		}
		event := requireSearchReadEvent(t, pool, operatorID, current.verb, current.reference)
		requireSearchReadPayload(t, event, payload, targets)
		events = append(events, event)
		returnedTargets[event.EventID] = targets
	}
	pipeline := newPartsPipeline(t)
	pipeline.startConsumer(t)
	pipeline.startRelay(t, nil)
	ledger := publicLedger{stack: pipeline.stack(), since: clock.Now().Add(-time.Hour)}
	for _, event := range events {
		row := pipeline.stack().waitForRow(t, event.EventID)
		requireSearchReadLedgerIdentity(t, row, event)
		if row.Outcome != event.Outcome || row.Context.OperationID != event.Context.OperationID ||
			row.Context.Read == nil || *row.Context.Read != *event.Context.Read {
			t.Fatalf("ledger event differs from finalized read %s: %+v", event.EventID, row)
		}
		if targets := ledger.manifestTargets(t, row); !slices.Equal(targets, returnedTargets[event.EventID]) {
			t.Fatalf("ledger targets = %+v, want %+v", targets, returnedTargets[event.EventID])
		}
		pipeline.requireActorPII(t, event.Actor, row)
		t.Logf("ledger event=%s operation=%s verb=%s outcome=%s count=%d sha256=%s",
			row.EventID, row.Context.OperationID, row.Action, row.Outcome, row.Context.Read.Count, row.Context.Read.PayloadSHA256)
	}
}

func searchVerifyPayloadTargets(t *testing.T, payload []byte) []audit.Target {
	t.Helper()
	var report struct {
		ExcludedNodes int `json:"excluded_nodes"`
		Exclusions    []struct {
			OrgID  string `json:"org_id"`
			NodeID string `json:"node_id"`
		} `json:"exclusions"`
		PropertyWarnings int `json:"property_warnings"`
		Warnings         []struct {
			OrgID        string `json:"org_id"`
			NodeID       string `json:"node_id"`
			PropertyName string `json:"property_name"`
		} `json:"warnings"`
		StuckWorkItems int `json:"stuck_work_items"`
	}
	if err := json.Unmarshal(payload, &report); err != nil {
		t.Fatalf("decode verify payload: %v", err)
	}
	if report.ExcludedNodes != len(report.Exclusions) || report.PropertyWarnings != len(report.Warnings) || report.StuckWorkItems != 0 {
		t.Fatalf("verify counts differ from the returned records: %+v", report)
	}
	var targets []audit.Target
	for _, exclusion := range report.Exclusions {
		targets = append(targets, searchReadResource("exclusions/"+exclusion.OrgID+"/"+exclusion.NodeID))
	}
	for _, warning := range report.Warnings {
		targets = append(targets, searchReadResource("warnings/"+warning.OrgID+"/"+warning.NodeID+"/"+url.PathEscape(warning.PropertyName)))
	}
	return targets
}

func searchReadResource(key string) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: "", URI: "tack:search/" + key,
		RelationType: "", ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

func createSearchReadDiagnostics(t *testing.T, fixture integration.QueryFixture) []string {
	t.Helper()
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entry := integration.EntryPoint(t, fixture, workspace)
	warned := integration.PutOpaqueNode(t, fixture, kind, entry, "read warning", "quartz lantern", "excluded")
	property := "audit/property key"
	storeUncheckedProperty(t, fixture, workspace.OrgID, warned, property, "undeclared value")
	orphan := uuid.Must(uuid.NewV7())
	now := clock.Now().UTC()
	value := &node.Node{ID: orphan, OrgID: workspace.OrgID, NodeType: kind.TypeKey, Name: "read orphan", CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: orphan, OrgID: workspace.OrgID, NodeType: kind.TypeKey, Name: "read orphan", CreatedAt: now, UpdatedAt: now}
	ctx := integration.WithTestOperation(t, context.WithoutCancel(t.Context()))
	if err := fixture.Stores.Nodes.CreateAtomic(ctx, value, view, nil, nil, nil, nil); err != nil {
		t.Fatalf("create orphan diagnostic source: %v", err)
	}
	integration.DrainSearchWork(t, fixture.Worker, 2000)
	return []string{
		"tack:search/exclusions/" + workspace.OrgID.String() + "/" + orphan.String(),
		"tack:search/warnings/" + workspace.OrgID.String() + "/" + warned.String() + "/" + url.PathEscape(property),
	}
}
