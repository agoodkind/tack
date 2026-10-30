package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	searchReadDeployCommit = "tack-617-read-regression"
	searchReadSession      = "tack-617-test-session"
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

func searchReadCommand(t *testing.T, cfg *config.Config, pool *pgxpool.Pool, operatorID uuid.UUID, command ...string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	factory := cli.System(cfg)
	output := &bytes.Buffer{}
	factory.Out, factory.Err = output, &bytes.Buffer{}
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	factory.SetAuditReads(audit.NewPoolReadLifecycle(pool))
	probeQuery := readTestSQL(t, "search_read_infrastructure.sql")
	factory.SetAuditInfrastructureProbe(func(ctx context.Context) (audit.InfrastructureState, error) {
		connection, err := pgx.Connect(ctx, cfg.DatabaseURL)
		if err != nil {
			return audit.InfrastructureState{}, err
		}
		defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
		var state audit.InfrastructureState
		err = connection.QueryRow(ctx, probeQuery).Scan(&state.OutboxTableExists, &state.OperatorLoginExists, &state.OperatorReadsExist)
		return state, err
	})
	root := integration.SearchCommandRoot(factory)
	root.SetArgs(append([]string{
		"--execute", "--output", "json", "--operator-id", operatorID.String(),
		"--operator-email", searchSettingsOperatorEmail, "--operator-name", "Search Test",
		"--operator-service", searchReadService(operatorID), "--operator-session", searchReadSession,
		"--deploy-commit", searchReadDeployCommit,
	}, command...))
	return root, output
}

func searchReadService(operatorID uuid.UUID) string { return "tack-search-read-" + operatorID.String() }

func requireSearchReadEvent(t *testing.T, pool *pgxpool.Pool, operatorID uuid.UUID, verb audit.Verb, reference string) audit.Event {
	t.Helper()
	actorID := cli.ServiceActorID(searchReadService(operatorID))
	events := readOutboxEvents(t, pool, verb, actorID)
	if len(events) != 1 {
		t.Fatalf("%s recorded %d events instead of one final read: %+v", verb, len(events), events)
	}
	event := events[0]
	if err := audit.ValidateEvent(event); err != nil {
		t.Fatalf("%s recorded an invalid final event: %v", verb, err)
	}
	if event.Actor.Type != audit.ActorService || event.Actor.ID != actorID || event.Actor.SessionID != searchReadSession ||
		event.Context.AuditFormatVersion != audit.AuditFormatComplete || event.Context.OperationClass != audit.OperationRead ||
		event.Context.Phase != audit.PhaseRead || event.Context.OperationID == uuid.Nil ||
		event.Context.StartedAt.IsZero() || event.Context.CompletedAt.Before(event.Context.StartedAt) ||
		!event.OccurredAt.Equal(event.Context.StartedAt) || event.Context.Target == nil ||
		event.Context.Target.Reference != reference {
		t.Fatalf("%s did not record the actual actor, target, operation, and times: %+v", verb, event)
	}
	var extra struct {
		commandExtra
		DeployCommit string          `json:"deploy_commit"`
		Input        json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(event.Extra, &extra); err != nil {
		t.Fatalf("decode %s operator evidence: %v", verb, err)
	}
	if extra.OpID != event.Context.OperationID || extra.DeployCommit != searchReadDeployCommit ||
		extra.SessionID != searchReadSession || extra.OnBehalfOf == nil || extra.OnBehalfOf.OperatorID != operatorID ||
		string(extra.Input) != "{}" {
		t.Fatalf("%s operator evidence = %+v, want the accountable operator, deployment, and safe input", verb, extra)
	}
	return event
}

func requireSearchReadPayload(t *testing.T, event audit.Event, payload []byte, targets []audit.Target) {
	t.Helper()
	digest, parts, err := audit.ReadManifestDigest(targets)
	if err != nil {
		t.Fatalf("digest expected read targets: %v", err)
	}
	evidence := event.Context.Read
	if event.Outcome != audit.OutcomeCompleted || evidence == nil || evidence.Count != uint64(len(targets)) ||
		evidence.PayloadSHA256 != payloadDigest(payload) || evidence.PayloadFormat != "application/json" ||
		evidence.ManifestSHA256 != digest || evidence.ManifestParts != parts {
		t.Fatalf("%s read evidence = %+v, want the exact payload and ordered targets %+v", event.Verb, evidence, targets)
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
