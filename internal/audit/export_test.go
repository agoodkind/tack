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
			OrgID:       uuid.Must(uuid.NewV7()),
			EventTime:   now,
			EventID:     uuid.Must(uuid.NewV7()),
			Seq:         1,
			Shard:       1,
			ActorID:     uuid.Must(uuid.NewV7()),
			ActorKind:   1,
			Action:      "node.read",
			Outcome:     OutcomeOK,
			EntityID:    uuid.Must(uuid.NewV7()),
			Context:     json.RawMessage(`{"request_id":"req-export","trace_id":"trace-export"}`),
			HashVersion: auditHashVersion3,
		},
	}
	rows[0].RowHash = hashExportTestRow(t, rows[0])
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

func TestVerifyBundleReportsEditedRow(t *testing.T) {
	dir := t.TempDir()
	row := Row{
		OrgID: uuid.Must(uuid.NewV7()), EventTime: time.Now().UTC(), EventID: uuid.Must(uuid.NewV7()),
		Seq: 1, Shard: 1, ActorID: uuid.Must(uuid.NewV7()), ActorKind: 1,
		Action: "node.read", Outcome: OutcomeOK, EntityKind: "node", EntityID: uuid.Must(uuid.NewV7()),
		Context: json.RawMessage(`{"request_id":"req-tamper"}`), HashVersion: auditHashVersion3,
	}
	row.RowHash = hashExportTestRow(t, row)
	pub := writeSignedExportTestBundle(t, dir, []Row{row})

	jsonlPath := filepath.Join(dir, "events.jsonl")
	jsonlBytes, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	var edited Row
	if err := json.Unmarshal(jsonlBytes, &edited); err != nil {
		t.Fatal(err)
	}
	edited.Action = "node.delete"
	editedBytes, err := json.Marshal(edited)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonlPath, append(editedBytes, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.HashMatches != 0 {
		t.Fatalf("hash matches = %d, want 0", report.HashMatches)
	}
	if len(report.ChainBreaks) == 0 || !strings.Contains(report.ChainBreaks[0], row.EventID.String()) {
		t.Fatalf("chain breaks = %v, want row %s", report.ChainBreaks, row.EventID)
	}
}

func hashExportTestRow(t *testing.T, row Row) []byte {
	t.Helper()
	hash, err := hashRowForEvent(rowHashInput{
		Event: Event{
			Verb: row.Action, EventID: row.EventID,
			Actor:   Actor{Type: actorTypeFromCode(row.ActorKind), ID: row.ActorID},
			Entity:  Entity{Type: row.EntityKind, ID: row.EntityID},
			Context: EventContext{OrgID: row.OrgID}, Outcome: row.Outcome,
			Error: nil, Extra: row.Extra, OccurredAt: row.EventTime,
			IdempotencyKey: row.IdempotencyKey,
		},
		EventID: row.EventID, Shard: row.Shard, Seq: row.Seq, ContextJSON: row.Context,
		DeltaJSON: row.Delta, LastHash: row.PrevHash, Version: row.HashVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func writeSignedExportTestBundle(t *testing.T, dir string, rows []Row) ed25519.PublicKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(dir, "events.jsonl")
	file, err := os.Create(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if err := json.NewEncoder(file).Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	jsonlBytes, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	manifest := ExportManifest{
		ExportID: uuid.Must(uuid.NewV7()), OrgID: rows[0].OrgID,
		Oldest: now.Add(-time.Hour), Latest: now.Add(time.Hour), RowCount: len(rows),
		FileSHA256: hex.EncodeToString(sumSHA256(jsonlBytes)), SignatureBy: "test",
	}
	signable, err := json.Marshal(struct {
		ExportID    uuid.UUID `json:"export_id"`
		OrgID       uuid.UUID `json:"org_id"`
		Oldest      time.Time `json:"oldest"`
		Latest      time.Time `json:"latest"`
		RowCount    int       `json:"row_count"`
		FileSHA256  string    `json:"file_sha256"`
		SignatureBy string    `json:"signing_key_id"`
	}{manifest.ExportID, manifest.OrgID, manifest.Oldest, manifest.Latest, manifest.RowCount, manifest.FileSHA256, manifest.SignatureBy})
	if err != nil {
		t.Fatal(err)
	}
	manifest.Signature = hex.EncodeToString(ed25519.Sign(priv, signable))
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), manifestBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return pub
}

// TestVerifyBundleRecoversLegacyRows pins the TACK-445 story for rows written
// under hash versions 1 and 2: their hash covered a nanosecond timestamp the
// database stored only to the microsecond, so the verifier recovers the lost
// remainder by trying every possible value. The row below reproduces the real
// condition: its stored hash was computed from a nanosecond timestamp, and
// the row read back carries the microsecond-truncated one.
func TestVerifyBundleRecoversLegacyRows(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	originalTime := time.Date(2026, 8, 10, 4, 17, 1, 59766789, time.UTC)
	row := Row{
		OrgID: orgID, EventTime: originalTime, EventID: uuid.Must(uuid.NewV7()),
		Seq: 1, Shard: 1, ActorID: uuid.Must(uuid.NewV7()), ActorKind: 5,
		Action: "ops.test.legacy", Outcome: OutcomeOK, EntityKind: "system", EntityID: orgID,
		Context: json.RawMessage(`{}`), HashVersion: auditHashVersion2,
	}
	row.RowHash = hashExportTestRow(t, row)
	row.EventTime = originalTime.Truncate(time.Microsecond)
	pub := writeSignedExportTestBundle(t, dir, []Row{row})

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.HashMatches != 1 {
		t.Fatalf("hash matches = %d, want 1; the candidate search must recover the legacy row", report.HashMatches)
	}
	if verdict := report.Err(); verdict != nil {
		t.Fatalf("honest legacy bundle rejected: %v", verdict)
	}
}

// TestVerifyBundleRejectsHashVersionDowngrade reproduces the review attack:
// edit a version 3 row's content and relabel it version 2, leaving both
// hashes untouched. The relabel buys the forger nothing, because a claimed
// version 2 row is recomputed at every possible lost-nanosecond candidate and
// the edited content matches none of them.
func TestVerifyBundleRejectsHashVersionDowngrade(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	rows := make([]Row, 0, 2)
	var prevHash []byte
	for seq := int64(1); seq <= 2; seq++ {
		row := Row{
			OrgID: orgID, EventTime: time.Now().UTC(), EventID: uuid.Must(uuid.NewV7()),
			Seq: seq, Shard: 1, ActorID: uuid.Must(uuid.NewV7()), ActorKind: 5,
			Action: "ops.test.downgrade", Outcome: OutcomeOK, EntityKind: "system", EntityID: orgID,
			Context: json.RawMessage(`{}`), HashVersion: auditHashVersion3,
			PrevHash: prevHash,
		}
		row.RowHash = hashExportTestRow(t, row)
		prevHash = row.RowHash
		rows = append(rows, row)
	}
	rows[1].Action = "ops.test.forged"
	rows[1].HashVersion = auditHashVersion2
	pub := writeSignedExportTestBundle(t, dir, rows)

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	verdict := report.Err()
	if verdict == nil {
		t.Fatal("downgraded tampered row accepted, want a rejection")
	}
	if !strings.Contains(verdict.Error(), "hash") {
		t.Fatalf("verdict = %v, want it to name the hash failure", verdict)
	}
	if len(report.ChainBreaks) == 0 || !strings.Contains(report.ChainBreaks[0], rows[1].EventID.String()) {
		t.Fatalf("chain breaks = %v, want the relabeled row %s named", report.ChainBreaks, rows[1].EventID)
	}
}

// TestVerifyReportErrTracksTheReport pins that the verdict a caller acts on
// matches the report a human reads. A clean bundle returns nil; a tampered one
// returns an error naming what failed. Before this, verification printed its
// findings and reported success, so a script or a release gate treated a
// tampered bundle as verified.
func TestVerifyReportErrTracksTheReport(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	row := Row{
		OrgID: orgID, EventTime: time.Now().UTC(), EventID: uuid.Must(uuid.NewV7()),
		Seq: 1, Shard: 1, ActorID: uuid.Must(uuid.NewV7()), ActorKind: 5,
		Action: "ops.test.verdict", Outcome: OutcomeOK, EntityKind: "system", EntityID: orgID,
		Context: json.RawMessage(`{}`), HashVersion: auditHashVersion3,
	}
	row.RowHash = hashExportTestRow(t, row)
	pub := writeSignedExportTestBundle(t, dir, []Row{row})

	clean, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("verify clean bundle: %v", err)
	}
	if verdict := clean.Err(); verdict != nil {
		t.Fatalf("clean bundle rejected: %v", verdict)
	}

	jsonlPath := filepath.Join(dir, "events.jsonl")
	jsonlBytes, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	var edited Row
	if err := json.Unmarshal(jsonlBytes, &edited); err != nil {
		t.Fatal(err)
	}
	edited.Action = "ops.test.tampered"
	editedBytes, err := json.Marshal(edited)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonlPath, append(editedBytes, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	tampered, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("verify tampered bundle: %v", err)
	}
	verdict := tampered.Err()
	if verdict == nil {
		t.Fatal("tampered bundle accepted, want a rejection")
	}
	if !strings.Contains(verdict.Error(), "hash") {
		t.Fatalf("verdict = %v, want it to name the hash failure", verdict)
	}
}

// TestVerifyOneEditedRowDoesNotCondemnTheRest pins that editing a single row
// is reported as a single failure. Advancing the chain tracking only on a
// matching row made the next row compare against the wrong predecessor, so
// one edit cascaded into a break on every row after it, and a reader could
// not tell how much of the bundle had actually been altered.
func TestVerifyOneEditedRowDoesNotCondemnTheRest(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	rows := make([]Row, 0, 4)
	var prevHash []byte
	for seq := int64(1); seq <= 4; seq++ {
		row := Row{
			OrgID: orgID, EventTime: time.Now().UTC(), EventID: uuid.Must(uuid.NewV7()),
			Seq: seq, Shard: 1, ActorID: uuid.Must(uuid.NewV7()), ActorKind: 5,
			Action: "ops.test.chain", Outcome: OutcomeOK, EntityKind: "system", EntityID: orgID,
			Context: json.RawMessage(`{}`), HashVersion: auditHashVersion3,
			PrevHash: prevHash,
		}
		row.RowHash = hashExportTestRow(t, row)
		prevHash = row.RowHash
		rows = append(rows, row)
	}
	pub := writeSignedExportTestBundle(t, dir, rows)

	jsonlPath := filepath.Join(dir, "events.jsonl")
	original, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(original), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("bundle lines = %d, want 4", len(lines))
	}
	var edited Row
	if err := json.Unmarshal([]byte(lines[1]), &edited); err != nil {
		t.Fatal(err)
	}
	edited.Action = "ops.test.tampered"
	editedLine, err := json.Marshal(edited)
	if err != nil {
		t.Fatal(err)
	}
	lines[1] = string(editedLine)
	if err := os.WriteFile(jsonlPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.HashMatches != 3 {
		t.Fatalf("hash matches = %d, want 3 of 4 rows still intact", report.HashMatches)
	}
	if len(report.ChainBreaks) != 1 {
		t.Fatalf("chain breaks = %v, want exactly the one edited row", report.ChainBreaks)
	}
	if !strings.Contains(report.ChainBreaks[0], edited.EventID.String()) {
		t.Fatalf("break = %q, want it to name the edited row %s",
			report.ChainBreaks[0], edited.EventID)
	}
	// This assertion is what catches the cascade. The rows are contiguous, so
	// a correct pass reports no gaps. Tracking that advanced only on a match
	// would leave the sequence stuck at the last good row, and every row after
	// the edit would be counted as a gap.
	if report.ChainGapCount != 0 {
		t.Fatalf("sequence gaps = %d in a contiguous bundle, want 0", report.ChainGapCount)
	}
	if report.Err() == nil {
		t.Fatal("tampered bundle accepted")
	}
}
