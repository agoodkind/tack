package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// searchSettingsReport is the result that ops search settings writes.
type searchSettingsReport struct {
	Command    string `json:"command"`
	Persistent string `json:"persistent"`
	Transient  string `json:"transient"`
	Defaults   string `json:"defaults"`
	Effective  string `json:"effective"`
}

// TestSearchSettingsReportsEffectivePointInTimeLimit runs the audited ops
// search settings command against the shared engine. The engine starts with
// search.max_open_pit_context as a node setting. Without a cluster setting,
// the command must report the node setting as the default and as the
// effective value. A persistent cluster setting must replace the effective
// value, and a transient cluster setting must replace the persistent one.
// OpenSearch omits the default while a cluster setting exists. Each run
// must write one audit row of the settings verb to the operator outbox.
// Cleanup removes both cluster settings.
func TestSearchSettingsReportsEffectivePointInTimeLimit(t *testing.T) {
	fixture := testenv.OpenSearch(t)
	cfg := integration.SearchControlConfig(t, fixture)
	client := integration.OpenSearchClient(t, fixture)
	putPointInTimeSetting(t, client, "persistent", "null")
	putPointInTimeSetting(t, client, "transient", "null")
	t.Cleanup(func() {
		putPointInTimeSetting(t, client, "persistent", "null")
		putPointInTimeSetting(t, client, "transient", "null")
	})
	node := testenv.OpenSearchPointInTimeLimit
	requireSearchSettings(t, cfg, searchSettingsReport{
		Command: "ops.search.settings", Persistent: "", Transient: "", Defaults: node, Effective: node,
	})
	putPointInTimeSetting(t, client, "persistent", "2500")
	requireSearchSettings(t, cfg, searchSettingsReport{
		Command: "ops.search.settings", Persistent: "2500", Transient: "", Defaults: "", Effective: "2500",
	})
	putPointInTimeSetting(t, client, "transient", "2000")
	requireSearchSettings(t, cfg, searchSettingsReport{
		Command: "ops.search.settings", Persistent: "2500", Transient: "2000", Defaults: "", Effective: "2000",
	})
}

// requireSearchSettings runs ops search settings as a new operator and
// requires the result to contain exactly the fields of want with the values
// of want. It then requires the outbox to hold one ok row of the settings
// verb for that operator, with an operation ID.
func requireSearchSettings(t *testing.T, cfg *config.Config, want searchSettingsReport) {
	t.Helper()
	pool, err := pgxpool.New(integration.WithTestOperation(t, t.Context()), testenv.Ledger(t))
	if err != nil {
		t.Fatalf("open audit ledger pool: %v", err)
	}
	defer pool.Close()
	operatorID := uuid.Must(uuid.NewV7())
	raw := runSearchSettingsCommand(t, cfg, pool, operatorID)
	requireSearchSettingsAudit(t, pool, operatorID)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode ops search settings fields %s: %v", raw, err)
	}
	wantFields := []string{"command", "defaults", "effective", "persistent", "transient"}
	if got := slices.Sorted(maps.Keys(fields)); !slices.Equal(got, wantFields) {
		t.Fatalf("ops search settings fields = %v, want %v", got, wantFields)
	}
	var got searchSettingsReport
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode ops search settings result %s: %v", raw, err)
	}
	if got != want {
		t.Fatalf("ops search settings result = %+v, want %+v", got, want)
	}
}

// requireSearchSettingsAudit reads the outbox rows of the settings verb for
// operatorID through the real SQL outbox. The read choke-point writes one
// row with the ok outcome before the read runs. The test requires exactly
// that one row, a non-nil operation ID in Extra.op_id, and the operator
// identity that the command received.
func requireSearchSettingsAudit(t *testing.T, pool *pgxpool.Pool, operatorID uuid.UUID) {
	t.Helper()
	events := outboxEventsByQuery(t, pool, "runtime_audit_verb_outbox.sql", string(audit.VerbOpsSearchSettings), operatorID.String())
	if len(events) != 1 {
		t.Fatalf("ops search settings outbox rows for operator %s = %d, want 1: %+v", operatorID, len(events), events)
	}
	event := events[0]
	if event.Outcome != audit.OutcomeOK {
		t.Fatalf("ops search settings audit outcome = %q, want %q", event.Outcome, audit.OutcomeOK)
	}
	if operationID := decodeCommandExtra(t, event).OpID; operationID == uuid.Nil {
		t.Fatalf("ops search settings audit row %s has no Extra.op_id", event.EventID)
	}
	if event.Actor.ID != operatorID || event.Actor.Email != searchSettingsOperatorEmail {
		t.Fatalf("ops search settings audit actor = %s %q, want %s %q",
			event.Actor.ID, event.Actor.Email, operatorID, searchSettingsOperatorEmail)
	}
}

// searchSettingsOperatorEmail is the operator email that each settings run
// passes to the command.
const searchSettingsOperatorEmail = "operator@example.com"

// runSearchSettingsCommand runs ops search settings as operatorID through the
// rendered Cobra tree with the real SQL outbox on pool and returns the result
// payload.
func runSearchSettingsCommand(t *testing.T, cfg *config.Config, pool *pgxpool.Pool, operatorID uuid.UUID) json.RawMessage {
	t.Helper()
	factory := cli.System(cfg)
	var output bytes.Buffer
	factory.Out = &output
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	root := integration.SearchCommandRoot(factory)
	root.SetArgs([]string{
		"--execute", "--output", "json", "--operator-id", operatorID.String(),
		"--operator-email", searchSettingsOperatorEmail, "--operator-name", "Search Test",
		"ops", "search", "settings",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("ops search settings: %v\n%s", err, output.String())
	}
	var document struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatalf("decode ops search settings output %q: %v", output.String(), err)
	}
	return document.Result
}

// putPointInTimeSetting writes search.max_open_pit_context in one cluster
// settings section through the cluster settings API. A value of null
// removes the setting from that section.
func putPointInTimeSetting(t *testing.T, client *opensearchapi.Client, section, value string) {
	t.Helper()
	body := `{"` + section + `":{"search.max_open_pit_context":` + value + `}}`
	ctx := context.WithoutCancel(integration.WithTestOperation(t, t.Context()))
	response, err := client.Cluster.PutSettings(ctx, &opensearchapi.ClusterPutSettingsReq{
		BodyReader: strings.NewReader(body), Header: nil, Params: &opensearchapi.ClusterPutSettingsParams{},
	})
	if err != nil {
		t.Fatalf("set %s search.max_open_pit_context to %s: %v", section, value, err)
	}
	if !response.Acknowledged {
		t.Fatalf("OpenSearch did not acknowledge %s search.max_open_pit_context %s", section, value)
	}
}
