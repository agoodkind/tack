package audit

import (
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
	"strings"
	"time"

	"github.com/google/uuid"
)

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

// Err returns an error naming every integrity check the bundle failed, and nil
// when the bundle verified. A caller prints the report either way; this is
// what a caller that reads only success or failure needs, so a tampered bundle
// cannot pass silently through a script, a scheduled job, or a release gate.
func (r *VerifyReport) Err() error {
	var failures []string
	if !r.FileSHA256OK {
		failures = append(failures, "the events file digest does not match the manifest")
	}
	if !r.SignatureOK {
		failures = append(failures, "the manifest signature did not verify")
	}
	if len(r.ChainBreaks) > 0 {
		failures = append(failures,
			fmt.Sprintf("%d chain break(s), first: %s", len(r.ChainBreaks), r.ChainBreaks[0]))
	}
	// A sequence gap is deliberately not a failure. An export is filtered by
	// time, actor, or entity, so a legitimate bundle routinely omits rows and
	// the sequence skips. Treating that as tampering would reject almost
	// every honest export, and a verifier that cries wolf gets ignored. The
	// row hash and the previous-hash link are what actually detect a change,
	// and both are checked above. The gap count stays in the report so a
	// reader can see it.
	// A row that scanned but did not match its stored hash shows up as a
	// shortfall in the counts even when nothing else complains.
	if r.HashMatches != r.RowsScanned {
		failures = append(failures,
			fmt.Sprintf("%d of %d rows failed their hash check",
				r.RowsScanned-r.HashMatches, r.RowsScanned))
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("bundle %s failed verification: %s",
		r.BundleDir, strings.Join(failures, "; "))
}

// VerifyBundle re-hashes every row in <dir>/events.jsonl, checks the chain
// links via prev_hash within each (org, shard) sequence, and validates the
// manifest signature with the supplied public key. Pure offline check.
func VerifyBundle(dir string, pub ed25519.PublicKey) (*VerifyReport, error) {
	report := &VerifyReport{
		BundleDir: dir, RowsScanned: 0, HashMatches: 0, ChainGapCount: 0,
		ChainBreaks: nil, FileSHA256OK: false, SignatureOK: false,
		ManifestSubject: "",
	}

	mfBytes, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		slog.Error("audit.verify.manifest_read_failed", slog.String("dir", dir), slog.String("err", err.Error()))
		return nil, fmt.Errorf("verify manifest: %w", err)
	}
	var mf ExportManifest
	if err := json.Unmarshal(mfBytes, &mf); err != nil {
		slog.Error("audit.verify.manifest_parse_failed", slog.String("dir", dir), slog.String("err", err.Error()))
		return nil, fmt.Errorf("verify manifest parse: %w", err)
	}
	report.ManifestSubject = mf.ExportID.String()

	jsonlBytes, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		slog.Error("audit.verify.events_read_failed", slog.String("dir", dir), slog.String("err", err.Error()))
		return nil, fmt.Errorf("verify jsonl: %w", err)
	}
	gotHash := sha256.Sum256(jsonlBytes)
	report.FileSHA256OK = hex.EncodeToString(gotHash[:]) == mf.FileSHA256

	manifestForVerify, err := json.Marshal(struct {
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
	if err != nil {
		slog.Error("audit.verify.manifest_marshal_failed", slog.String("dir", dir), slog.String("err", err.Error()))
		return nil, fmt.Errorf("verify manifest marshal: %w", err)
	}
	sig, err := hex.DecodeString(mf.Signature)
	if err == nil && pub != nil {
		report.SignatureOK = ed25519.Verify(pub, manifestForVerify, sig)
	}

	dec := json.NewDecoder(newLineReader(jsonlBytes))
	// A bundle row carrying a key its type does not declare is rejected, not
	// dropped: the typed decode is what the hash is recomputed from, so a
	// silently dropped key would be a planted field that verifies.
	dec.DisallowUnknownFields()
	rows := make([]Row, 0, mf.RowCount)
	for {
		var row Row
		err := dec.Decode(&row)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			slog.Error("audit.verify.row_decode_failed", slog.String("dir", dir), slog.Int("line", len(rows)+1), slog.String("err", err.Error()))
			return report, fmt.Errorf("verify decode events.jsonl line %d: %w", len(rows)+1, err)
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
			// A gap is counted, not reported as a break. An export is
			// filtered, so missing sequence numbers are the normal case and
			// say nothing about tampering.
			if row.Seq != lastSeqByShard[row.Shard]+1 {
				report.ChainGapCount++
			}
			if row.Seq == lastSeqByShard[row.Shard]+1 && !bytesEqual(row.PrevHash, lastHashByShard[row.Shard]) {
				report.ChainBreaks = append(report.ChainBreaks,
					fmt.Sprintf("row %s has previous-hash link mismatch", row.EventID))
			}
		}
		matched, reason, err := checkRowHash(row)
		if err != nil {
			return err
		}
		if matched {
			report.HashMatches++
		} else {
			report.ChainBreaks = append(report.ChainBreaks,
				fmt.Sprintf("row %s %s", row.EventID, reason))
		}
		// Tracking advances for every row, matched or not. Advancing only on
		// a match would make the next row compare itself against a row that
		// is not its predecessor, so one edited row would report every row
		// after it as broken too, and a reader could not tell how much of the
		// bundle was actually altered.
		lastSeqByShard[row.Shard] = row.Seq
		lastHashByShard[row.Shard] = row.RowHash
		seenShard[row.Shard] = true
	}
	return nil
}
