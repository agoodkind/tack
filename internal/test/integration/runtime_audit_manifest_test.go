package integration_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
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

// requireManifestTamperRejected exports a bundle and requires verification
// to report a read manifest failure for each missing, reordered, or altered
// part. The altered payload includes a matching part digest.
func (stack runtimeAuditStack) requireManifestTamperRejected(t *testing.T, orgID uuid.UUID, oldest, latest time.Time, eventID uuid.UUID) {
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
	mutations := map[string]func([]audit.ManifestPart) []audit.ManifestPart{
		"missing": func(parts []audit.ManifestPart) []audit.ManifestPart { return parts[:len(parts)-1] },
		"reordered": func(parts []audit.ManifestPart) []audit.ManifestPart {
			parts[0], parts[1] = parts[1], parts[0]
			return parts
		},
		"altered": func(parts []audit.ManifestPart) []audit.ManifestPart {
			parts[0].Payload = bytes.Replace(parts[0].Payload, []byte(`"issue"`), []byte(`"epic"`), 1)
			digest := sha256.Sum256(parts[0].Payload)
			parts[0].Digest = hex.EncodeToString(digest[:])
			return parts
		},
	}
	for name, mutate := range mutations {
		tampered := tamperManifestRow(t, original, eventID, mutate)
		if err := os.WriteFile(eventsPath, tampered, 0o600); err != nil {
			t.Fatalf("write the %s manifest rows: %v", name, err)
		}
		report, err := audit.VerifyBundle(dir, public)
		if err != nil {
			t.Fatalf("verify the %s manifest bundle: %v", name, err)
		}
		if !strings.Contains(strings.Join(report.ChainBreaks, "\n"), "read manifest") {
			t.Fatalf("verification of a %s manifest part = %v, want a read manifest failure", name, report.ChainBreaks)
		}
	}
}

// tamperManifestRow applies one change to the manifest parts of the selected
// event in the exported rows.
func tamperManifestRow(t *testing.T, rows []byte, eventID uuid.UUID, mutate func([]audit.ManifestPart) []audit.ManifestPart) []byte {
	t.Helper()
	lines := bytes.Split(bytes.TrimSuffix(rows, []byte("\n")), []byte("\n"))
	output := make([]byte, 0, len(rows))
	for _, line := range lines {
		var row audit.Row
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatalf("decode an exported row: %v", err)
		}
		if row.EventID == eventID {
			if len(row.ManifestParts) < 2 {
				t.Fatalf("exported row %s has %d manifest parts, want at least 2", eventID, len(row.ManifestParts))
			}
			row.ManifestParts = mutate(row.ManifestParts)
			encoded, err := json.Marshal(row)
			if err != nil {
				t.Fatalf("encode the tampered row: %v", err)
			}
			line = encoded
		}
		output = append(append(output, line...), '\n')
	}
	return output
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
