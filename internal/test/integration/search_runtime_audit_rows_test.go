//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// sessionChanges returns the change rows of verb on nodeType targets of
// sessionID, ordered by event time.
func sessionChanges(rows []audit.Row, sessionID uuid.UUID, verb audit.Verb, nodeType string) []audit.Row {
	selected := []audit.Row{}
	for _, row := range rows {
		target := row.Context.Target
		if row.Action == string(verb) && row.Context.Phase == audit.PhaseChange && target != nil &&
			target.NodeType == nodeType && strings.Contains(target.URI, sessionID.String()) {
			selected = append(selected, row)
		}
	}
	slices.SortFunc(selected, func(left, right audit.Row) int { return left.EventTime.Compare(right.EventTime) })
	return selected
}

func onlyRow(t *testing.T, rows []audit.Row, description string) audit.Row {
	t.Helper()
	if len(rows) != 1 {
		t.Fatalf("%s: found %d rows, want 1: %s", description, len(rows), describeRows(rows))
	}
	return rows[0]
}

// stepRows returns the rows of verb on the external step of sessionID.
func stepRows(rows []audit.Row, sessionID uuid.UUID, verb audit.Verb, action string) []audit.Row {
	reference := "opensearch/point-in-time/" + sessionID.String() + "/" + action
	selected := []audit.Row{}
	for _, row := range rows {
		target := row.Context.Target
		if row.Action == string(verb) && target != nil && target.Reference == reference {
			selected = append(selected, row)
		}
	}
	return selected
}

// finishedStep returns the terminal step row in rows.
func finishedStep(t *testing.T, rows []audit.Row) audit.Row {
	t.Helper()
	finished := []audit.Row{}
	for _, row := range rows {
		if row.Context.Phase == audit.PhaseResult {
			finished = append(finished, row)
		}
	}
	return onlyRow(t, finished, "the finished point in time step")
}

// refusedReads returns the node.search rows with the refused outcome.
func refusedReads(rows []audit.Row) []audit.Row {
	refused := []audit.Row{}
	for _, row := range rows {
		if row.Action == string(audit.VerbNodeSearch) && row.Outcome == audit.OutcomeRefused {
			refused = append(refused, row)
		}
	}
	return refused
}

func deltaMembers(t *testing.T, value json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	members := map[string]json.RawMessage{}
	if err := json.Unmarshal(value, &members); err != nil {
		t.Fatalf("decode delta value %s: %v", value, err)
	}
	return members
}

// memberReference returns the protected reference of member.
func memberReference(t *testing.T, members map[string]json.RawMessage, member string) uuid.UUID {
	t.Helper()
	var reference audit.ProtectedReference
	if err := json.Unmarshal(members[member], &reference); err != nil || reference.PIIRef == uuid.Nil {
		t.Fatalf("member %s = %s, want a protected reference: %v", member, members[member], err)
	}
	return reference.PIIRef
}

// requireProtectedMember requires member to reference an unredacted audit.pii
// value equal to want.
func (f sessionAuditFixture) requireProtectedMember(t *testing.T, members map[string]json.RawMessage, member string, want []byte) {
	t.Helper()
	reference := memberReference(t, members, member)
	stored, found := protectedValues(t, f.redactorPool, []uuid.UUID{reference})[reference]
	if !found || stored.Redacted || !bytes.Equal(stored.Value, want) {
		t.Fatalf("audit.pii value of member %s = %+v, found %t, want %s", member, stored, found, want)
	}
}

func encodeJSON[Value any](t *testing.T, value Value) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %v: %v", value, err)
	}
	return encoded
}
