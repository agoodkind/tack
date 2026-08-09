package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Export streams the events that match filter into a directory layout:
//
//	<dir>/events.jsonl       newline-delimited Row records
//	<dir>/manifest.json      {export_id, org_id, time_range, row_count,
//	                          file_sha256, signature, signing_key_id}
//
// Caller supplies the audit Reader and the same Ed25519 signing key the
// notarizer uses; verification reuses the public half. Returns the manifest
// as a Go struct so the caller can render it.
type ExportManifest struct {
	ExportID    uuid.UUID `json:"export_id"`
	OrgID       uuid.UUID `json:"org_id"`
	Oldest      time.Time `json:"oldest"`
	Latest      time.Time `json:"latest"`
	RowCount    int       `json:"row_count"`
	FileSHA256  string    `json:"file_sha256"`
	SignatureBy string    `json:"signing_key_id"`
	Signature   string    `json:"signature"`
}

// Export writes events.jsonl + manifest.json under dir and returns the
// manifest.
func Export(ctx context.Context, reader *Reader, signer ed25519.PrivateKey, keyID string, filter QueryFilter, dir string) (*ExportManifest, error) {
	if reader == nil {
		return nil, errors.New("audit export: reader required")
	}
	if signer == nil || keyID == "" {
		return nil, errors.New("audit export: signing key required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("audit export mkdir: %w", err)
	}

	rows, err := reader.Query(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("audit export query: %w", err)
	}

	jsonlPath := filepath.Join(dir, "events.jsonl")
	f, err := os.Create(jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("audit export open: %w", err)
	}
	hasher := sha256.New()
	writer := io.MultiWriter(f, hasher)
	enc := json.NewEncoder(writer)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("audit export encode: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("audit export close: %w", err)
	}

	manifest := &ExportManifest{
		ExportID:    uuid.Must(uuid.NewV7()),
		OrgID:       filter.OrgID,
		Oldest:      filter.Oldest,
		Latest:      filter.Latest,
		RowCount:    len(rows),
		FileSHA256:  hex.EncodeToString(hasher.Sum(nil)),
		SignatureBy: keyID,
	}
	manifestForSign, _ := json.Marshal(struct {
		ExportID    uuid.UUID `json:"export_id"`
		OrgID       uuid.UUID `json:"org_id"`
		Oldest      time.Time `json:"oldest"`
		Latest      time.Time `json:"latest"`
		RowCount    int       `json:"row_count"`
		FileSHA256  string    `json:"file_sha256"`
		SignatureBy string    `json:"signing_key_id"`
	}{
		ExportID:    manifest.ExportID,
		OrgID:       manifest.OrgID,
		Oldest:      manifest.Oldest,
		Latest:      manifest.Latest,
		RowCount:    manifest.RowCount,
		FileSHA256:  manifest.FileSHA256,
		SignatureBy: manifest.SignatureBy,
	})
	sig := ed25519.Sign(signer, manifestForSign)
	manifest.Signature = hex.EncodeToString(sig)

	mfPath := filepath.Join(dir, "manifest.json")
	mfBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("audit export marshal manifest: %w", err)
	}
	if err := os.WriteFile(mfPath, mfBytes, 0o600); err != nil {
		return nil, fmt.Errorf("audit export write manifest: %w", err)
	}
	return manifest, nil
}

// VerifyReport summarises the result of validating an exported bundle.
type VerifyReport struct {
	BundleDir       string
	RowsScanned     int
	HashMatches     int
	ChainGapCount   int
	ChainBreaks     []string
	FileSHA256OK    bool
	SignatureOK     bool
	ManifestSubject string
}

// VerifyBundle re-hashes every row in <dir>/events.jsonl, checks the chain
// links via prev_hash within each (org, shard) sequence, and validates the
// manifest signature with the supplied public key. Pure offline check.
func VerifyBundle(dir string, pub ed25519.PublicKey) (*VerifyReport, error) {
	report := &VerifyReport{BundleDir: dir}

	mfBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("verify manifest: %w", err)
	}
	var mf ExportManifest
	if err := json.Unmarshal(mfBytes, &mf); err != nil {
		return nil, fmt.Errorf("verify manifest parse: %w", err)
	}
	report.ManifestSubject = mf.ExportID.String()

	jsonlBytes, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("verify jsonl: %w", err)
	}
	gotHash := sha256.Sum256(jsonlBytes)
	report.FileSHA256OK = hex.EncodeToString(gotHash[:]) == mf.FileSHA256

	manifestForVerify, _ := json.Marshal(struct {
		ExportID    uuid.UUID `json:"export_id"`
		OrgID       uuid.UUID `json:"org_id"`
		Oldest      time.Time `json:"oldest"`
		Latest      time.Time `json:"latest"`
		RowCount    int       `json:"row_count"`
		FileSHA256  string    `json:"file_sha256"`
		SignatureBy string    `json:"signing_key_id"`
	}{
		ExportID:    mf.ExportID,
		OrgID:       mf.OrgID,
		Oldest:      mf.Oldest,
		Latest:      mf.Latest,
		RowCount:    mf.RowCount,
		FileSHA256:  mf.FileSHA256,
		SignatureBy: mf.SignatureBy,
	})
	sig, err := hex.DecodeString(mf.Signature)
	if err == nil && pub != nil {
		report.SignatureOK = ed25519.Verify(pub, manifestForVerify, sig)
	}

	dec := json.NewDecoder(newLineReader(jsonlBytes))
	rows := make([]Row, 0, mf.RowCount)
	for {
		var row Row
		err := dec.Decode(&row)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return report, fmt.Errorf("verify decode: %w", err)
		}
		rows = append(rows, row)
	}
	report.RowsScanned = len(rows)
	if err := verifyExportRows(report, rows); err != nil {
		return report, err
	}
	return report, nil
}

func verifyExportRows(report *VerifyReport, rows []Row) error {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Shard != rows[j].Shard {
			return rows[i].Shard < rows[j].Shard
		}
		return rows[i].Seq < rows[j].Seq
	})
	lastSeqByShard := map[int16]int64{}
	lastHashByShard := map[int16][]byte{}
	seenShard := map[int16]bool{}
	for _, row := range rows {
		if seenShard[row.Shard] {
			if row.Seq != lastSeqByShard[row.Shard]+1 {
				report.ChainGapCount++
				report.ChainBreaks = append(report.ChainBreaks,
					fmt.Sprintf("row %s has sequence gap at shard %d sequence %d", row.EventID, row.Shard, row.Seq))
			}
			if row.Seq == lastSeqByShard[row.Shard]+1 && !bytesEqual(row.PrevHash, lastHashByShard[row.Shard]) {
				report.ChainBreaks = append(report.ChainBreaks,
					fmt.Sprintf("row %s has previous-hash link mismatch", row.EventID))
			}
		}
		piiRef := uuid.Nil
		if row.PIIRef != nil {
			piiRef = *row.PIIRef
		}
		eventError, err := unmarshalEventError(row.Error)
		if err != nil {
			slog.Error("audit.export.row_error_decode_failed", slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
			return fmt.Errorf("verify row %s: %w", row.EventID, err)
		}
		expected, err := hashRowForEvent(rowHashInput{
			Event:   exportEvent(row, eventError),
			EventID: row.EventID, Shard: row.Shard, Seq: row.Seq,
			PIIRef: piiRef, ContextJSON: row.Context, DeltaJSON: row.Delta,
			LastHash: row.PrevHash, Version: row.HashVersion,
		})
		if err != nil {
			slog.Error("audit.export.hash_failed", slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
			return fmt.Errorf("verify hash row %s: %w", row.EventID, err)
		}
		if !bytesEqual(expected, row.RowHash) {
			report.ChainBreaks = append(report.ChainBreaks,
				fmt.Sprintf("row %s hash mismatch", row.EventID))
			continue
		}
		report.HashMatches++
		lastSeqByShard[row.Shard] = row.Seq
		lastHashByShard[row.Shard] = row.RowHash
		seenShard[row.Shard] = true
	}
	return nil
}

func unmarshalEventError(raw json.RawMessage) (*EventError, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var eventError EventError
	if err := json.Unmarshal(raw, &eventError); err != nil {
		slog.Error("audit.export.event_error_decode_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("encoding/json error: %w", err)
	}
	return &eventError, nil
}

func exportEvent(row Row, eventError *EventError) Event {
	return Event{
		Verb: row.Action, EventID: row.EventID,
		Actor: Actor{
			Type: actorTypeFromCode(row.ActorKind), ID: row.ActorID,
			Email: "", Name: "", SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: Entity{
			Type: row.EntityKind, NodeType: "", ID: row.EntityID, Identifier: "", Name: "",
		},
		Context: EventContext{
			OrgID: row.OrgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: "", Tool: "", RPC: "", Reason: "",
		},
		Delta: nil, Outcome: row.Outcome, Error: eventError,
		IdempotencyKey: row.IdempotencyKey, OccurredAt: row.EventTime, Extra: row.Extra,
	}
}

func actorTypeFromCode(code int16) ActorType {
	switch code {
	case 1:
		return ActorUser
	case 2:
		return ActorService
	case 3:
		return ActorSystem
	case 4:
		return ActorToken
	case 5:
		return ActorOperator
	default:
		return ""
	}
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// newLineReader returns an io.Reader over b. Helper to keep the JSON decoder
// happy without pulling in bytes.NewReader at call sites.
func newLineReader(b []byte) io.Reader {
	return &bytesReader{b: b}
}

type bytesReader struct {
	b []byte
	i int
}

func (r *bytesReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
