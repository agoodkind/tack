package integration_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

// lastChangeRelationType is the relation type of the relationships that the
// last-change test adds and removes.
const lastChangeRelationType = "blocks"

// lastChangeReport is the result member of the `audit last-change` output.
type lastChangeReport struct {
	Truncated            bool                    `json:"truncated"`
	HistoryStartInWindow bool                    `json:"history_start_in_window"`
	Fields               []audit.LastChangeEntry `json:"fields"`
	Relationships        []audit.LastChangeEntry `json:"relationships"`
}

// lastChangeRows stores the ledger rows that the last-change assertions use.
type lastChangeRows struct {
	updates    []audit.Row
	removal    audit.Row
	reverseAdd audit.Row
	refused    audit.Row
}

// recordLegacyIssueUpdate produces a copy of the legacy node.update fixture
// for issueID at occurredAt to the ledger topic and returns its event ID.
func recordLegacyIssueUpdate(t *testing.T, ledger *nodeAuditLedger, orgID, issueID uuid.UUID, occurredAt time.Time) uuid.UUID {
	t.Helper()
	createEventPartition(t, ledger.admin, occurredAt)
	for _, fixture := range legacyFixtures(t, "bb9792cd", "kafka") {
		if fixture.Verb != string(audit.VerbNodeUpdate) {
			continue
		}
		var event audit.Event
		if err := json.Unmarshal([]byte(fixture.Value), &event); err != nil {
			t.Fatalf("decode legacy fixture %s: %v", fixture.EventID, err)
		}
		event.EventID = uuid.Must(uuid.NewV7())
		event.Entity.ID = issueID
		event.Context.OrgID = orgID
		event.OccurredAt = occurredAt
		recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
			Brokers: []string{ledger.cfg.AuditKafkaBrokers}, Topic: ledger.cfg.AuditKafkaTopic, ClientID: "", ProduceTimeout: 0,
		})
		if err != nil {
			t.Fatalf("open the Kafka recorder: %v", err)
		}
		defer func() { _ = recorder.Close() }()
		if err := recorder.Record(t.Context(), event); err != nil {
			t.Fatalf("record legacy event %s: %v", event.EventID, err)
		}
		return event.EventID
	}
	t.Fatal("the bb9792cd fixtures do not contain a node.update Kafka record")
	return uuid.Nil
}

// waitForLastChangeRows polls the ledger until it contains the four update
// rows, the legacy row, both relationship rows, and the refused update.
func waitForLastChangeRows(t *testing.T, ledger *nodeAuditLedger, orgID, issueID, legacyID uuid.UUID) lastChangeRows {
	t.Helper()
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		var found lastChangeRows
		legacySeen := false
		for _, row := range ledger.orgRows(t, orgID, issueID, "") {
			legacySeen = legacySeen || row.EventID == legacyID
			if row.Action == string(audit.VerbNodeUpdate) && row.Context.Phase == audit.PhaseChange {
				found.updates = append(found.updates, row)
			}
		}
		for _, row := range ledger.orgRows(t, orgID, uuid.Nil, audit.VerbRelationshipRemove) {
			if relationshipChange(row) && row.Context.Target.SourceID == issueID {
				found.removal = row
			}
		}
		for _, row := range ledger.orgRows(t, orgID, uuid.Nil, audit.VerbRelationshipAdd) {
			if relationshipChange(row) && row.Context.Target.TargetID == issueID {
				found.reverseAdd = row
			}
		}
		for _, row := range ledger.orgRows(t, orgID, uuid.Nil, audit.VerbNodeUpdate) {
			if row.Outcome == audit.OutcomeRefused || row.Outcome == audit.OutcomeFailed {
				found.refused = row
			}
		}
		complete := len(found.updates) == 4 && legacySeen && found.removal.EventID != uuid.Nil &&
			found.reverseAdd.EventID != uuid.Nil && found.refused.EventID != uuid.Nil
		if complete {
			return found
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger rows after %s: %d updates, legacy %t, removal %s, reverse add %s, refused %s",
				nodeAuditDeadline, len(found.updates), legacySeen, found.removal.EventID, found.reverseAdd.EventID, found.refused.EventID)
		}
		time.Sleep(nodeAuditPollInterval)
	}
}

// relationshipChange reports whether row is a change row of a relationship.
func relationshipChange(row audit.Row) bool {
	target := row.Context.Target
	return row.Context.Phase == audit.PhaseChange && target != nil && target.Kind == audit.TargetKindRelationship &&
		target.RelationType == lastChangeRelationType
}

// orgRows reads at most one page of the ledger rows of orgID from the last
// hour, limited to entityID and action when they are set.
func (l *nodeAuditLedger) orgRows(t *testing.T, orgID, entityID uuid.UUID, action audit.Verb) []audit.Row {
	t.Helper()
	page, err := l.reader.QueryPage(t.Context(), audit.QueryFilter{
		OrgID: orgID, Oldest: clock.Now().Add(-time.Hour), Latest: clock.Now().Add(time.Hour),
		Action: string(action), ActorID: uuid.Nil, EntityID: entityID, RequestID: "", TraceID: "", Limit: audit.MaxQueryPageLimit,
	}, "")
	if err != nil {
		t.Fatalf("read the ledger rows of org %s: %v", orgID, err)
	}
	return page.Rows
}

// runLastChange runs `audit last-change` for issueID through the built server
// with the audit_reader login of ledger and decodes the result.
func runLastChange(t *testing.T, ledger *nodeAuditLedger, orgID, issueID uuid.UUID, oldest, latest time.Time) lastChangeReport {
	t.Helper()
	ledgerDSN := testenv.Ledger(t)
	logs := t.TempDir()
	process := exec.CommandContext(t.Context(), buildMigrateServer(t),
		"--execute", "--operator-id", uuid.Must(uuid.NewV7()).String(), "--operator-email", "last-change@example.test",
		"--output", "json", "audit", "last-change", "--org", orgID.String(), "--entity-id", issueID.String(),
		"--oldest", oldest.UTC().Format(time.RFC3339), "--latest", latest.UTC().Format(time.RFC3339))
	process.Env = append(os.Environ(),
		"DATABASE_URL="+ledgerDSN, "FDB_CLUSTER_FILE="+testenv.FoundationDB(t), "ENV=development",
		"AUDIT_OPERATOR_DSN="+ledgerDSN, "AUDIT_READER_DSN="+ledger.cfg.AuditReaderDSN, "LOG_DISABLE_STDOUT=true",
		"LOG_JSON_FILE="+filepath.Join(logs, "tack.json"), "LOG_TEXT_FILE="+filepath.Join(logs, "tack.log"))
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	process.Stdout, process.Stderr = stdout, stderr
	if err := process.Run(); err != nil {
		t.Fatalf("audit last-change failed: %v\n%s%s", err, stdout, stderr)
	}
	var envelope struct {
		Result lastChangeReport `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode the audit last-change output: %v\n%s", err, stdout)
	}
	return envelope.Result
}
