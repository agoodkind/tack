//go:build runtimeaudit

package integration_test

import (
	"encoding/hex"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// requireCaseEvidence checks each format 2 row that has the verb and outcome
// of the case. Each row must record a target, and the target must match the
// node of the case when the case acted on one. A completed read row must
// record a SHA-256 payload digest, and the digest must match the response
// body when the case returned one.
func requireCaseEvidence(t *testing.T, evidence coverageEvidence, verb audit.Verb, outcome audit.Outcome, rows []audit.Row) {
	t.Helper()
	for _, row := range rows {
		if row.Action != string(verb) || row.Outcome != outcome || row.Context.AuditFormatVersion != audit.AuditFormatComplete {
			continue
		}
		requireCaseTarget(t, evidence, row)
		if row.Context.OperationClass != audit.OperationRead || row.Outcome != audit.OutcomeCompleted {
			continue
		}
		read := row.Context.Read
		if read == nil || !isPayloadDigest(read.PayloadSHA256) {
			t.Fatalf("Completed read event %s did not record a payload digest; the read evidence follows: %+v", row.EventID, read)
		}
		if evidence.body != nil && read.PayloadSHA256 != payloadDigest(evidence.body) {
			t.Fatalf("Completed read event %s recorded payload digest %s, but the response body has digest %s.",
				row.EventID, read.PayloadSHA256, payloadDigest(evidence.body))
		}
	}
}

// requireCaseTarget requires row to record a target. When the case acted on
// a node, the target ID or the relationship source ID must equal that node.
func requireCaseTarget(t *testing.T, evidence coverageEvidence, row audit.Row) {
	t.Helper()
	target := row.Context.Target
	if target == nil || target.Kind == "" {
		t.Fatalf("Format 2 event %s did not record a target; the event context follows: %+v", row.EventID, row.Context)
	}
	if evidence.targetID != uuid.Nil && target.ID != evidence.targetID && target.SourceID != evidence.targetID {
		t.Fatalf("Event %s recorded target %+v, but the case acted on %s.", row.EventID, *target, evidence.targetID)
	}
}

// isPayloadDigest reports whether digest is a lowercase hexadecimal SHA-256
// digest.
func isPayloadDigest(digest string) bool {
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == digest
}

// argumentNodeID returns the node_id argument of an MCP tool call, or
// uuid.Nil when the call does not include a valid node_id.
func argumentNodeID(arguments map[string]any) uuid.UUID {
	value, isString := arguments["node_id"].(string)
	if !isString {
		return uuid.Nil
	}
	nodeID, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil
	}
	return nodeID
}
