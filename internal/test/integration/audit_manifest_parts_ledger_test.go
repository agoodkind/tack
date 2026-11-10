package integration_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// requireStoredAuditReads polls the ledger until the operator has one audit.read row per run. It fails when the operator has more rows or the deadline passes.
func requireStoredAuditReads(
	t *testing.T,
	ledger *nodeAuditLedger,
	operatorID uuid.UUID,
	runs []manifestReadRun,
) {
	t.Helper()
	requireRowsMatchRuns(t, ledger, operatorAuditReadRows(t, ledger, operatorID, len(runs)), runs)
}

// requireRowsMatchRuns matches each row to a run by safe input and evidence. It checks the returned identities.
func requireRowsMatchRuns(
	t *testing.T,
	ledger *nodeAuditLedger,
	rows []audit.Row,
	runs []manifestReadRun,
) {
	t.Helper()
	remaining := slices.Clone(runs)

	for _, row := range rows {
		var extra struct {
			Input struct {
				Org            string `json:"org"`
				EventID        string `json:"event_id"`
				VerifyPayloads bool   `json:"verify_payloads"`
			} `json:"input"`
		}
		if err := json.Unmarshal(row.Extra, &extra); err != nil {
			t.Fatalf("decode the extra of row %s: %v\n%s", row.EventID, err, row.Extra)
		}

		position := slices.IndexFunc(
			remaining,
			func(run manifestReadRun) bool {
				return run.orgInput == extra.Input.Org && run.eventInput == extra.Input.EventID &&
					run.verifyPayloads == extra.Input.VerifyPayloads &&
					manifestRowMatches(t, row, run)
			},
		)
		if position < 0 {
			t.Fatalf(
				"row %s does not match a remaining run: %+v context %+v error %+v, runs %+v",
				row.EventID,
				extra.Input,
				row.Context,
				row.Error,
				remaining,
			)
		}

		requireReturnedIdentities(t, ledger, row, remaining[position])
		remaining = slices.Delete(remaining, position, position+1)
	}
}

// manifestRowMatches reports whether a row matches the outcome, class, target, and read evidence of a run. It fails the test when a failed row stores forbidden text.
func manifestRowMatches(t *testing.T, row audit.Row, run manifestReadRun) bool {
	t.Helper()
	eventContext := row.Context
	if row.Outcome != run.outcome || eventContext.OperationClass != audit.OperationRead ||
		eventContext.Target == nil || eventContext.Target.URI != run.targetURI {
		return false
	}

	if run.outcome != audit.OutcomeCompleted {
		if eventContext.Read != nil || row.Error == nil ||
			(run.errorCode != "" && row.Error.Code != run.errorCode) ||
			!strings.Contains(row.Error.Message, run.messageContains) {
			return false
		}

		for _, forbidden := range run.forbidden {
			if strings.Contains(row.Error.Message, forbidden) ||
				strings.Contains(string(row.Extra), forbidden) {
				t.Fatalf(
					"row %s stores the text %q: error %+v, extra %s",
					row.EventID,
					forbidden,
					row.Error,
					row.Extra,
				)
			}
		}

		return true
	}

	if eventContext.Read == nil || eventContext.Read.PayloadSHA256 != run.payloadSHA256 ||
		eventContext.Read.Count != uint64(len(run.partURIs)) {
		return false
	}

	manifestDigest, manifestParts := expectedManifestEvidence(t, run.partURIs)

	return eventContext.Read.ManifestSHA256 == manifestDigest &&
		eventContext.Read.ManifestParts == manifestParts
}

// expectedManifestEvidence returns the manifest digest and part count for the part URIs.
func expectedManifestEvidence(t *testing.T, partURIs []string) (string, uint32) {
	t.Helper()
	if len(partURIs) == 0 {
		return "", 0
	}

	targets := make([]audit.Target, 0, len(partURIs))
	for _, uri := range partURIs {
		targets = append(
			targets,
			audit.Target{
				Kind:         audit.TargetKindResource,
				NodeType:     "",
				Reference:    "",
				Name:         "",
				URI:          uri,
				RelationType: "",
				ID:           uuid.Nil,
				SourceID:     uuid.Nil,
				TargetID:     uuid.Nil,
			},
		)
	}

	manifestDigest, manifestParts, err := audit.ReadManifestDigest(targets)
	if err != nil {
		t.Fatalf("compute the expected read manifest digest: %v", err)
	}

	return manifestDigest, manifestParts
}

// requireReturnedIdentities reads a row's stored read manifest and compares its target URIs with the part URIs of the run.
func requireReturnedIdentities(
	t *testing.T,
	ledger *nodeAuditLedger,
	row audit.Row,
	run manifestReadRun,
) {
	t.Helper()
	parts, err := ledger.reader.ManifestParts(t.Context(), audit.SystemOrgID(), row.EventID)
	if err != nil {
		t.Fatalf("read the manifest parts of row %s: %v", row.EventID, err)
	}

	uris := make([]string, 0, len(run.partURIs))

	for _, part := range parts {
		var targets []audit.Target
		if err := json.Unmarshal(part.Payload, &targets); err != nil {
			t.Fatalf("decode manifest part %d of row %s: %v", part.Index, row.EventID, err)
		}

		for _, target := range targets {
			uris = append(uris, target.URI)
		}
	}

	if !slices.Equal(uris, run.partURIs) {
		t.Fatalf("row %s returned identities = %v, want %v", row.EventID, uris, run.partURIs)
	}
}

// storeManifestRead writes a completed read event and its targets to the outbox. It returns the event ID and stored parts after the reader finds at least two declared parts.
func storeManifestRead(
	t *testing.T,
	ledger *nodeAuditLedger,
	orgID uuid.UUID,
) (uuid.UUID, []audit.ManifestPart) {
	t.Helper()
	first := readNodeTarget()
	targets := syntheticReadTargets(first)
	manifestDigest, manifestParts, err := audit.ReadManifestDigest(targets)
	if err != nil {
		t.Fatalf("compute the read manifest digest: %v", err)
	}

	payloadDigest := sha256.Sum256([]byte(first.ID.String()))
	read := completeEvent(
		orgID,
		userActor(),
		first,
		audit.OperationRead,
		audit.PhaseRead,
		audit.OutcomeCompleted,
	)
	read.Context.Read = &audit.ReadEvidence{
		Count:          readTargetCount,
		PayloadSHA256:  hex.EncodeToString(payloadDigest[:]),
		PayloadFormat:  "application/json",
		ManifestSHA256: manifestDigest,
		ManifestParts:  manifestParts,
	}
	encoded, err := audit.EncodeEvent(read, targets)
	if err != nil {
		t.Fatalf("encode event %s: %v", read.EventID, err)
	}

	transaction, err := ledger.admin.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin the outbox write of event %s: %v", read.EventID, err)
	}

	defer func() { _ = transaction.Rollback(context.WithoutCancel(t.Context())) }()
	if err := audit.WriteEncodedOutboxTx(t.Context(), transaction, encoded); err != nil {
		t.Fatalf("write event %s to the outbox: %v", read.EventID, err)
	}

	if err := transaction.Commit(t.Context()); err != nil {
		t.Fatalf("commit the outbox write of event %s: %v", read.EventID, err)
	}

	deadline := clock.Now().Add(nodeAuditDeadline)

	for {
		parts, err := ledger.reader.ManifestParts(t.Context(), orgID, read.EventID)
		if err != nil {
			t.Fatalf("read the manifest parts of event %s: %v", read.EventID, err)
		}

		if uint32(len(parts)) == manifestParts && manifestParts >= 2 {
			return read.EventID, parts
		}

		if clock.Now().After(deadline) {
			t.Fatalf(
				"event %s stored %d manifest parts after %s, want %d",
				read.EventID,
				len(parts),
				nodeAuditDeadline,
				manifestParts,
			)
		}

		time.Sleep(nodeAuditPollInterval)
	}
}
