package servercli

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// TestAuditVerifyCommandFailsOnTamperedBundle checks that the command returns
// an error for a mismatched row hash, even with a valid signature and signer.
// Audit-package verdict tests alone do not check that the command uses the verdict.
func TestAuditVerifyCommandFailsOnTamperedBundle(t *testing.T) {
	dir := t.TempDir()
	keyPath := writeVerifyTestKey(t, dir)
	bundleDir := filepath.Join(dir, "bundle")
	if err := os.MkdirAll(bundleDir, 0o750); err != nil {
		t.Fatalf("create bundle dir: %v", err)
	}

	writeMismatchedBundle(t, bundleDir, keyPath)

	_, keyID, err := loadAuditKey(keyPath)
	if err != nil {
		t.Fatalf("load key: %v", err)
	}

	factory := &cli.Factory{Cfg: nil, In: nil, Out: os.Stdout, Err: os.Stderr}
	operation := auditVerifyOp(factory)
	// Include the verifying key in the set to isolate row-hash rejection.
	input := auditVerifyInput{
		InputMarker: clispec.InputMarker{},
		Bundle:      bundleDir,
		Pub:         keyPath,
		Signers:     keyID,
	}

	err = operation.Run(context.Background(), input, clispec.NewCLISink(factory))
	if err == nil {
		t.Fatal("command accepted a mismatched row hash; expected failure")
	}

	if !strings.Contains(err.Error(), "failed their hash check") ||
		strings.Contains(err.Error(), "signer") {
		t.Fatalf("error = %v, want a row-hash failure without a signer failure", err)
	}
}

func writeVerifyTestKey(t *testing.T, dir string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	path := filepath.Join(dir, "audit-signing.pem")
	if err := os.WriteFile(
		path,
		pem.EncodeToMemory(&pem.Block{
			Type:  "PRIVATE KEY",
			Bytes: der,
		}),
		0o600,
	); err != nil {
		t.Fatalf("write key: %v", err)
	}

	return path
}

// writeMismatchedBundle writes a signed bundle with a mismatched stored row
// hash. A valid signature and file digest isolate the row-hash check.
func writeMismatchedBundle(t *testing.T, bundleDir, keyPath string) {
	t.Helper()
	priv, keyID, err := loadAuditKey(keyPath)
	if err != nil {
		t.Fatalf("load key: %v", err)
	}

	orgID := uuid.Must(uuid.NewV7())
	row := audit.Row{
		OrgID:      orgID,
		EventTime:  time.Now().UTC(),
		EventID:    uuid.Must(uuid.NewV7()),
		Seq:        1,
		Shard:      1,
		ActorID:    uuid.Must(uuid.NewV7()),
		ActorKind:  5,
		Action:     "ops.test.verify",
		Outcome:    audit.OutcomeOK,
		EntityKind: "system",
		EntityID:   orgID,
		// Version 3 computes one hash and rejects a mismatch.
		Context:     audit.EventContext{OrgID: orgID, Source: audit.SourceSystem},
		HashVersion: 3,
		RowHash:     []byte("this is not the hash of this row"),
	}
	rowBytes, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}

	jsonl := append(rowBytes, '\n')
	if err := os.WriteFile(filepath.Join(bundleDir, "events.jsonl"), jsonl, 0o600); err != nil {
		t.Fatalf("write events: %v", err)
	}

	digest := sha256.Sum256(jsonl)
	now := time.Now().UTC()
	manifest := audit.ExportManifest{
		ExportID: uuid.Must(uuid.NewV7()),
		OrgID:    orgID,
		Oldest:   now.Add(-time.Hour),
		Latest:   now.Add(time.Hour),
		RowCount: 1,
		// Identify the actual signing key to isolate row-hash rejection.
		FileSHA256:  hex.EncodeToString(digest[:]),
		SignatureBy: keyID,
	}
	signable, err := json.Marshal(
		struct {
			ExportID    uuid.UUID `json:"export_id"`
			OrgID       uuid.UUID `json:"org_id"`
			Oldest      time.Time `json:"oldest"`
			Latest      time.Time `json:"latest"`
			RowCount    int       `json:"row_count"`
			FileSHA256  string    `json:"file_sha256"`
			SignatureBy string    `json:"signing_key_id"`
		}{
			manifest.ExportID,
			manifest.OrgID,
			manifest.Oldest,
			manifest.Latest,
			manifest.RowCount,
			manifest.FileSHA256,
			manifest.SignatureBy,
		},
	)
	if err != nil {
		t.Fatalf("marshal signable manifest: %v", err)
	}

	manifest.Signature = hex.EncodeToString(ed25519.Sign(priv, signable))
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(bundleDir, "manifest.json"),
		manifestBytes,
		0o600,
	); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}
