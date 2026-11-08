package integration_test

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

type datagenReportFixture struct {
	config   *config.Config
	pipeline partsPipeline
	ledger   publicLedger
}

func newDatagenReportFixture(t *testing.T) datagenReportFixture {
	t.Helper()
	pipeline := newPartsPipeline(t)
	t.Setenv("DATABASE_URL", pipeline.ledger)
	t.Setenv("FDB_CLUSTER_FILE", testenv.FoundationDB(t))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load generated-data configuration: %v", err)
	}
	cfg.AuditReaderDSN, cfg.AuditKafkaBrokers, cfg.AuditKafkaTopic = pipeline.ledger, pipeline.brokers, pipeline.topic
	cfg.DatagenAllowTarget = "local"
	pipeline.startConsumer(t)
	ledger := startPublicLedgerOn(t, &publicServer{clusterFile: cfg.FDBClusterFile}, pipeline.stack())
	return datagenReportFixture{config: cfg, pipeline: pipeline, ledger: ledger}
}

// TestDatagenReportAudit finalizes each seed report and checks the permanent
// evidence against its returned payload, aggregate identities, and selection.
func TestDatagenReportAudit(t *testing.T) {
	fixture := newDatagenReportFixture(t)
	for _, format := range []string{cli.FormatJSON, cli.FormatText} {
		t.Run(format, func(t *testing.T) { runDatagenReportAudit(t, fixture, format) })
	}
}

func runDatagenReportAudit(t *testing.T, fixture datagenReportFixture, format string) {
	t.Helper()
	operatorID := uuid.Must(uuid.NewV7())
	seed := time.Now().UnixNano()
	root, output := searchReadCommand(t, fixture.config, fixture.pipeline.admin, operatorID,
		"ops", "qa", "datagen", "seed", "--commit", "--seed", strconv.FormatInt(seed, 10), "--output", format)
	ctx := testenv.CommandContext(t)
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatalf("run the seed report command: %v\n%s", err, output.String())
	}
	document := printedDatagenDocument(t, output.Bytes(), format)
	var result map[string]json.RawMessage
	if err := json.Unmarshal(document.Result, &result); err != nil {
		t.Fatalf("decode the seed reports: %v", err)
	}
	rows := fixture.operatorRows(t, operatorID, 9)
	parent := requireDatagenMutationOutcome(t, rows, audit.OutcomeOK, audit.VerbOpsDatagenSeed)
	fields := map[string]string{
		"ops.audit.consumer_offsets": "audit_consumer_offsets", "ops.qa.datagen.ledger_verbs": "ledger_verbs",
		"ops.qa.datagen.search_access_verbs": "search_access_verbs", "ops.qa.datagen.search_settings_verbs": "search_settings_verbs",
		"ops.qa.datagen.commit_order":          "commit_order",
		"ops.qa.datagen.manifest_verification": "manifest_verification",
	}
	seen := map[string]bool{}
	for _, row := range rows {
		var extra datagenReportExtra
		if err := json.Unmarshal(row.Extra, &extra); err != nil {
			t.Fatalf("decode report selection: %v", err)
		}
		field, found := fields[extra.Input.Report]
		if !found {
			if extra.Input.Report != "" {
				t.Fatalf("unexpected permanent report %s", extra.Input.Report)
			}
			continue
		}
		if seen[extra.Input.Report] {
			t.Fatalf("duplicate permanent report %s", extra.Input.Report)
		}
		payload := result[field]
		requireDatagenReportEvidence(t, fixture, row, extra, payload, operatorID, parent, document.Meta, seed)
		seen[extra.Input.Report] = true
	}
	for report := range fields {
		if !seen[report] {
			t.Fatalf("the permanent ledger omitted report %s", report)
		}
	}
	requireDatagenCommittedChanges(t, fixture, seed)
}

func (fixture datagenReportFixture) operatorRows(t *testing.T, operatorID uuid.UUID, count int) []audit.Row {
	t.Helper()
	var rows []audit.Row
	waitFor(t, "permanent generated-data command evidence", func() bool {
		all, err := fixture.pipeline.reader.Query(t.Context(), audit.QueryFilter{
			OrgID: audit.SystemOrgID(), Oldest: fixture.ledger.since, Latest: time.Now().Add(time.Hour),
			Action: "", ActorID: serviceDatagenActor(operatorID), EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 100,
		})
		if err != nil {
			t.Fatalf("query permanent generated-data command evidence: %v", err)
		}
		rows = nil
		for _, row := range all {
			if row.Action == string(audit.VerbOpsDatagenSeed) || row.Context.OperationClass == audit.OperationRead {
				rows = append(rows, row)
			}
		}
		return len(rows) >= count
	})
	if len(rows) != count {
		t.Fatalf("permanent generated-data command events = %d, want %d: %s", len(rows), count, describeDatagenRows(rows))
	}
	return rows
}

func describeDatagenRows(rows []audit.Row) string {
	encoded, _ := json.Marshal(rows)
	return string(encoded)
}
