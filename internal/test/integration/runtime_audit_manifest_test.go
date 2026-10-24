package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// readTargetCount sets the number of synthetic read targets.
const readTargetCount = 300

// syntheticReadTargets returns readTargetCount targets, starting with the
// given target and filling the remainder with synthetic node targets.
func syntheticReadTargets(first audit.Target) []audit.Target {
	targets := make([]audit.Target, 0, readTargetCount)
	targets = append(targets, first)
	for len(targets) < readTargetCount {
		targets = append(targets, audit.Target{
			Kind: audit.TargetKindNode, NodeType: "issue", Reference: "", Name: "", URI: "",
			RelationType: "", ID: uuid.Must(uuid.NewV7()), SourceID: uuid.Nil, TargetID: uuid.Nil,
		})
	}
	return targets
}

// deliverEncoded uses EncodeEvent to write an event and its targets to the
// YugabyteDB outbox, then starts a relay for the stack's topic.
func (stack runtimeAuditStack) deliverEncoded(t *testing.T, event audit.Event, targets []audit.Target) {
	t.Helper()
	encoded, err := audit.EncodeEvent(event, targets)
	if err != nil {
		t.Fatalf("encode event %s: %v", event.EventID, err)
	}
	transaction, err := stack.admin.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin the outbox write: %v", err)
	}
	defer func() { _ = transaction.Rollback(context.WithoutCancel(t.Context())) }()
	if err := audit.WriteEncodedOutboxTx(t.Context(), transaction, encoded); err != nil {
		t.Fatalf("write event %s to the outbox: %v", event.EventID, err)
	}
	if err := transaction.Commit(t.Context()); err != nil {
		t.Fatalf("commit the outbox write: %v", err)
	}
	pipeline := partsPipeline{
		topic: stack.topic, group: "", brokers: testenv.Kafka(t), ledger: "", admin: stack.admin, reader: stack.reader,
	}
	pipeline.startRelay(t, nil)
}

// requireExportTamperRejected exports a bundle and edits one exported row
// for each tamper case. The cases remove, reorder, or alter the manifest
// parts of the read event readID, and alter the target or the delta of the
// change event changeID. Verification of each edited bundle must fail and
// must report the expected failure for the edited row.
func (stack runtimeAuditStack) requireExportTamperRejected(t *testing.T, orgID uuid.UUID, oldest, latest time.Time, readID, changeID uuid.UUID) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("signing key: %v", err)
	}
	dir := t.TempDir()
	filter := audit.QueryFilter{
		OrgID: orgID, Oldest: oldest, Latest: latest, Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil,
		RequestID: "", TraceID: "", Limit: 0,
	}
	manifest, err := audit.Export(t.Context(), stack.reader, private, audit.KeyIdentifier(public), filter, dir)
	if err != nil {
		t.Fatalf("export org %s: %v", orgID, err)
	}
	eventsPath := filepath.Join(dir, manifest.EventsFile)
	original, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read the exported rows: %v", err)
	}
	tampers := manifestTampers(t, readID)
	maps.Copy(tampers, changeTampers(t, changeID))
	for name, tamper := range tampers {
		tampered := tamperExportedRow(t, original, tamper.eventID, tamper.mutate)
		if err := os.WriteFile(eventsPath, tampered, 0o600); err != nil {
			t.Fatalf("write the %s rows: %v", name, err)
		}
		report, err := audit.VerifyBundle(dir, public)
		if err != nil {
			t.Fatalf("verify the %s bundle: %v", name, err)
		}
		if report.Err() == nil {
			t.Fatalf("verification of the %s bundle passed, want a failure", name)
		}
		rowPrefix := "row " + tamper.eventID.String() + " "
		reported := slices.ContainsFunc(report.ChainBreaks, func(chainBreak string) bool {
			return strings.HasPrefix(chainBreak, rowPrefix) && strings.Contains(chainBreak, tamper.failure)
		})
		if !reported {
			t.Fatalf("verification of the %s bundle = %v, want %q for row %s", name, report.ChainBreaks, tamper.failure, tamper.eventID)
		}
	}
}

// requireStoredManifest requires the reader to return the declared number
// of read manifest parts, with at least two parts.
func (stack runtimeAuditStack) requireStoredManifest(t *testing.T, row audit.Row) {
	t.Helper()
	parts, err := stack.reader.ManifestParts(context.WithoutCancel(t.Context()), row.OrgID, row.EventID)
	if err != nil {
		t.Fatalf("read the manifest parts of %s: %v", row.EventID, err)
	}
	var count uint32
	for range parts {
		count++
	}
	if row.Context.Read == nil || count != row.Context.Read.ManifestParts || count < 2 {
		t.Fatalf("event %s stored %d manifest parts, want %+v with at least 2", row.EventID, count, row.Context.Read)
	}
}
