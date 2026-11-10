package integration_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// lastChangeRelationType is the relation type of the relationships that the
// last-change test adds and removes.
const lastChangeRelationType = "blocks"

// lastChangeReport is the result member of the `audit last-change` output.
type lastChangeReport struct {
	Truncated                  bool                    `json:"truncated"`
	HistoryStartInWindow       bool                    `json:"history_start_in_window"`
	LegacyRelationshipEventIDs []uuid.UUID             `json:"legacy_relationship_event_ids"`
	Fields                     []audit.LastChangeEntry `json:"fields"`
	Relationships              []audit.LastChangeEntry `json:"relationships"`
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
	event := legacyFixtureEvent(t, orgID, occurredAt)
	event.Entity.ID = issueID
	return recordLegacyEvent(t, ledger, event)
}

// recordLegacyRelationshipAdd produces a legacy relationship.add row in the
// shape that builds before commit 7a85ade3 wrote: entity type relationship,
// the source node as the entity ID, the relation type as the identifier, and
// the target as the name. The ledger columns do not store the identifier or
// the name. It returns the event ID.
func recordLegacyRelationshipAdd(t *testing.T, ledger *nodeAuditLedger, orgID, sourceID, targetID uuid.UUID, occurredAt time.Time) uuid.UUID {
	t.Helper()
	event := legacyFixtureEvent(t, orgID, occurredAt)
	event.Verb = string(audit.VerbRelationshipAdd)
	event.Entity = audit.Entity{
		Type: "relationship", NodeType: "", ID: sourceID, Identifier: lastChangeRelationType, Name: targetID.String(),
	}
	event.Context.Tool = "tack_add_relationship"
	event.Delta = nil
	return recordLegacyEvent(t, ledger, event)
}

// legacyFixtureEvent returns a copy of the bb9792cd legacy node.update Kafka
// fixture with a new event ID, orgID, and occurredAt.
func legacyFixtureEvent(t *testing.T, orgID uuid.UUID, occurredAt time.Time) audit.Event {
	t.Helper()
	for _, fixture := range legacyFixtures(t, "bb9792cd", "kafka") {
		if fixture.Verb != string(audit.VerbNodeUpdate) {
			continue
		}
		var event audit.Event
		if err := json.Unmarshal([]byte(fixture.Value), &event); err != nil {
			t.Fatalf("decode legacy fixture %s: %v", fixture.EventID, err)
		}
		event.EventID = uuid.Must(uuid.NewV7())
		event.Context.OrgID = orgID
		event.OccurredAt = occurredAt
		return event
	}
	t.Fatal("the bb9792cd fixtures do not contain a node.update Kafka record")
	return audit.Event{}
}

// recordLegacyEvent produces event to the ledger topic through the Kafka
// recorder and returns its event ID.
func recordLegacyEvent(t *testing.T, ledger *nodeAuditLedger, event audit.Event) uuid.UUID {
	t.Helper()
	createEventPartition(t, ledger.admin, event.OccurredAt)
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

// waitForLastChangeRows polls the ledger until it contains the four update
// rows, every legacy row in legacyIDs, both relationship rows, and the
// refused update.
func waitForLastChangeRows(t *testing.T, ledger *nodeAuditLedger, orgID, issueID uuid.UUID, legacyIDs ...uuid.UUID) lastChangeRows {
	t.Helper()
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		var found lastChangeRows
		legacySeen := 0
		for _, row := range ledger.orgRows(t, orgID, issueID, "") {
			if slices.Contains(legacyIDs, row.EventID) {
				legacySeen++
			}
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
		complete := len(found.updates) == 4 && legacySeen == len(legacyIDs) && found.removal.EventID != uuid.Nil &&
			found.reverseAdd.EventID != uuid.Nil && found.refused.EventID != uuid.Nil
		if complete {
			return found
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger rows after %s: %d updates, %d of %d legacy rows, removal %s, reverse add %s, refused %s",
				nodeAuditDeadline, len(found.updates), legacySeen, len(legacyIDs), found.removal.EventID, found.reverseAdd.EventID, found.refused.EventID)
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
