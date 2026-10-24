//go:build runtimeaudit

package integration_test

import (
	"encoding/hex"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// requireCaseEvidence checks each format 2 row that has the verb and outcome
// of the case. Each row must record a target. A completed read row must
// record a SHA-256 payload digest, and the digest must match the response
// body when the case returned one. When the case acted on a node, one row
// must target that node, or one completed read row must list it in its read
// manifest.
func requireCaseEvidence(t *testing.T, env *coverageEnv, evidence coverageEvidence, verb audit.Verb, outcome audit.Outcome, rows []audit.Row) {
	t.Helper()
	found := evidence.targetID == uuid.Nil
	for _, row := range rows {
		if row.Action != string(verb) || row.Outcome != outcome || row.Context.AuditFormatVersion != audit.AuditFormatComplete {
			continue
		}
		target := row.Context.Target
		if target == nil || target.Kind == "" {
			t.Fatalf("Format 2 event %s did not record a target; the event context follows: %+v", row.EventID, row.Context)
		}
		if !found && (target.ID == evidence.targetID || target.SourceID == evidence.targetID) {
			found = true
		}
		if row.Context.OperationClass != audit.OperationRead || row.Outcome != audit.OutcomeCompleted {
			continue
		}
		requireReadDigest(t, evidence, row)
		if !found && readManifestLists(env.ledger.manifestTargets(t, row), evidence.targetID) {
			found = true
		}
	}
	if !found {
		t.Fatalf("No %s %s event targeted node %s or listed it in a read manifest.", verb, outcome, evidence.targetID)
	}
}

// requireReadDigest requires the completed read row to record a SHA-256
// payload digest that matches the response body when the case has one.
func requireReadDigest(t *testing.T, evidence coverageEvidence, row audit.Row) {
	t.Helper()
	read := row.Context.Read
	if read == nil || !isPayloadDigest(read.PayloadSHA256) {
		t.Fatalf("Completed read event %s did not record a payload digest; the read evidence follows: %+v", row.EventID, read)
	}
	if evidence.body != nil && read.PayloadSHA256 != payloadDigest(evidence.body) {
		t.Fatalf("Completed read event %s recorded payload digest %s, but the response body has digest %s.",
			row.EventID, read.PayloadSHA256, payloadDigest(evidence.body))
	}
}

// readManifestLists reports whether targets contain nodeID as a target ID or
// a relationship source ID.
func readManifestLists(targets []audit.Target, nodeID uuid.UUID) bool {
	for _, target := range targets {
		if target.ID == nodeID || target.SourceID == nodeID {
			return true
		}
	}
	return false
}

// isPayloadDigest reports whether digest is a lowercase hexadecimal SHA-256
// digest.
func isPayloadDigest(digest string) bool {
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == digest
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
		t.Fatalf("The case passed node_id %v of type %T; the coverage check requires a UUID string.", raw, raw)
	}
	nodeID, err := uuid.Parse(value)
	if err != nil {
		t.Fatalf("The case passed node_id %q, which is not a UUID: %v", value, err)
	}
	return nodeID
}
