package audit

import (
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
)

// TestVerifyBundleRoundTrip writes a synthetic events.jsonl + manifest.json
// using the same shapes Export produces and asserts VerifyBundle accepts
// them. Avoids the database dependency at this layer; full Export is
// covered by the integration suite once Yugabyte is wired in test compose.
func TestVerifyBundleRoundTrip(t *testing.T) {
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	rows := []Row{
		{
			OrgID:     uuid.Must(uuid.NewV7()),
			EventTime: now,
			EventID:   uuid.Must(uuid.NewV7()),
			Seq:       1,
			Shard:     1,
			ActorID:   uuid.Must(uuid.NewV7()),
			Action:    "node.read",
			EntityID:  uuid.Must(uuid.NewV7()),
			Context:   json.RawMessage(`{"request_id":"req-export","trace_id":"trace-export"}`),
		},
	}
	jsonlPath := filepath.Join(dir, "events.jsonl")
	jf, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(jf)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	_ = jf.Close()
	jsonlBytes, _ := os.ReadFile(jsonlPath)
	hashHex := hex.EncodeToString(sumSHA256(jsonlBytes))

	manifest := ExportManifest{
		ExportID:    uuid.Must(uuid.NewV7()),
		OrgID:       rows[0].OrgID,
		Oldest:      now.Add(-time.Hour),
		Latest:      now.Add(time.Hour),
		RowCount:    len(rows),
		FileSHA256:  hashHex,
		SignatureBy: "ed25519:test",
	}
	signable, _ := json.Marshal(struct {
		ExportID    uuid.UUID `json:"export_id"`
		OrgID       uuid.UUID `json:"org_id"`
		Oldest      time.Time `json:"oldest"`
		Latest      time.Time `json:"latest"`
		RowCount    int       `json:"row_count"`
		FileSHA256  string    `json:"file_sha256"`
		SignatureBy string    `json:"signing_key_id"`
	}{
		manifest.ExportID, manifest.OrgID, manifest.Oldest, manifest.Latest,
		manifest.RowCount, manifest.FileSHA256, manifest.SignatureBy,
	})
	manifest.Signature = hex.EncodeToString(ed25519.Sign(priv, signable))
	mfBytes, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), mfBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !report.FileSHA256OK {
		t.Errorf("file sha mismatch")
	}
	if !report.SignatureOK {
		t.Errorf("signature did not verify")
	}
	if report.RowsScanned != 1 {
		t.Errorf("rows scanned = %d, want 1", report.RowsScanned)
	}
	var exported Row
	if err := json.Unmarshal(jsonlBytes, &exported); err != nil {
		t.Fatalf("exported row: %v", err)
	}
	if !strings.Contains(string(exported.Context), "req-export") {
		t.Fatalf("context did not export request_id: %s", exported.Context)
	}
}

func sumSHA256(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
