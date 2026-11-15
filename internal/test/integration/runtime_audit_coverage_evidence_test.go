//go:build runtimeaudit

package integration_test

import (
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// caseEvidenceFound reports whether rows contain an event with the verb and
// outcome of the case and, when the case acted on a node, a format 2 row that
// targets that node. It fails the test when a matching format 2 row does not
// record a target.
func caseEvidenceFound(
	t *testing.T,
	env *coverageEnv,
	evidence coverageEvidence,
	verb audit.Verb,
	outcome audit.Outcome,
	rows []audit.Row,
) bool {
	t.Helper()
	matched := false
	found := evidence.targetID == uuid.Nil
	for _, row := range rows {
		if row.Action != string(verb) || row.Outcome != outcome {
			continue
		}
		if row.Context.AuditFormatVersion == audit.AuditFormatComplete &&
			outcome == audit.OutcomeAttempted && row.Context.Phase != audit.PhaseAttempt {
			continue
		}
		matched = true
		if row.Context.AuditFormatVersion != audit.AuditFormatComplete {
			continue
		}
		target := row.Context.Target
		if target == nil || target.Kind == "" {
			t.Fatalf(
				"Format 2 event %s did not record a target; the event context follows: %+v",
				row.EventID,
				row.Context,
			)
		}
		if !found && (target.ID == evidence.targetID || target.SourceID == evidence.targetID) {
			found = true
		}
		if !found && row.Context.Selection != nil &&
			row.Context.Selection.Reference == evidence.targetID.String() {
			found = true
		}
	}
	return matched && found
}

// argumentNodeID returns the node_id argument of an MCP tool call, or
// uuid.Nil when the call does not include node_id. It fails the test when
// node_id is not a UUID string.
func argumentNodeID(t *testing.T, arguments map[string]any) uuid.UUID {
	t.Helper()
	raw, present := arguments["node_id"]
	if !present {
		return uuid.Nil
	}
	value, isString := raw.(string)
	if !isString {
		t.Fatalf(
			"The case passed node_id %v of type %T; the coverage check requires a UUID string.",
			raw,
			raw,
		)
	}
	nodeID, err := uuid.Parse(value)
	if err != nil {
		t.Fatalf("The case passed node_id %q, which is not a UUID: %v", value, err)
	}
	return nodeID
}
