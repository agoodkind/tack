package audit

import (
	"bufio"
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
	"strings"

	"github.com/google/uuid"
)

// VerifyReport summarises the result of validating an exported bundle.
type VerifyReport struct {
	BundleDir     string
	RowsScanned   int
	HashMatches   int
	ChainGapCount int
	ChainBreaks   []string
	FileSHA256OK  bool
	SignatureOK   bool
	// ManifestRowCount is what the manifest claims the file holds. It is
	// reported beside RowsScanned so a bundle whose own two halves disagree
	// fails on that disagreement, not only on the signature it also breaks.
	ManifestRowCount int
	ManifestSubject  string
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
	// The manifest names how many rows it signed for. A file that holds a
	// different number is either a manifest that was edited or an export
	// that stopped short of what it certified, and either way the bundle
	// does not describe itself truthfully.
	if r.ManifestRowCount != r.RowsScanned {
		failures = append(failures,
			fmt.Sprintf("the manifest declares %d rows but the events file holds %d",
				r.ManifestRowCount, r.RowsScanned))
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("bundle %s failed verification: %s",
		r.BundleDir, strings.Join(failures, "; "))
}

// chainLink is the only per-row state verification keeps after a row is
// scanned. A full Row carries parsed context, delta, and extra payloads, so
// holding every one of them made verification scale with total bundle bytes
// rather than row count: a 420,000-row production bundle exhausted the app
// container's memory limit and the verifier was killed (TACK-463). The link
// check needs nothing but the chain coordinates and the two hashes, which are
// fixed width, so this record is what the second pass reads instead.
type chainLink struct {
	Shard    int16
	Seq      int64
	EventID  uuid.UUID
	PrevHash [sha256.Size]byte
	RowHash  [sha256.Size]byte
}

// VerifyBundle re-hashes every row in <dir>/events.jsonl, checks the chain
// links via prev_hash within each (org, shard) sequence, and validates the
// manifest signature with the supplied public key. Pure offline check.
//
// The events file is streamed rather than read whole, and each row is hashed
// and released as it is decoded, so peak memory tracks the row count times a
// fixed-width link record instead of the size of the bundle.
func VerifyBundle(dir string, pub ed25519.PublicKey) (*VerifyReport, error) {
	report := &VerifyReport{
		BundleDir: dir, RowsScanned: 0, HashMatches: 0, ChainGapCount: 0,
		ChainBreaks: nil, FileSHA256OK: false, SignatureOK: false,
		ManifestRowCount: 0, ManifestSubject: "",
	}

	mf, err := readExportManifest(dir)
	if err != nil {
		return nil, err
	}
	report.ManifestSubject = mf.ExportID.String()
	report.ManifestRowCount = mf.RowCount
	report.SignatureOK = manifestSignatureOK(dir, mf, pub)

	links, err := scanBundleRows(dir, report, mf)
	if err != nil {
		return report, err
	}
	verifyChainLinks(report, links)
	return report, nil
}

// scanBundleRows streams events.jsonl once: it digests the bytes for the
// manifest check, decodes one row at a time, verifies that row's own hash, and
// keeps only its chain link. The decoded row goes out of scope immediately.
func scanBundleRows(dir string, report *VerifyReport, mf ExportManifest) ([]chainLink, error) {
	path := filepath.Join(dir, "events.jsonl")
	file, err := os.Open(path)
	if err != nil {
		slog.Error("audit.verify.events_read_failed", slog.String("dir", dir), slog.String("err", err.Error()))
		return nil, fmt.Errorf("verify jsonl: %w", err)
	}
	defer func() { _ = file.Close() }()

	hasher := sha256.New()
	reader := bufio.NewReaderSize(io.TeeReader(file, hasher), 1<<20)
	dec := json.NewDecoder(reader)
	// A bundle row carrying a key its type does not declare is rejected, not
	// dropped: the typed decode is what the hash is recomputed from, so a
	// silently dropped key would be a planted field that verifies.
	dec.DisallowUnknownFields()

	// The manifest's row count is not a size hint. It is unverified input
	// until the signature and the file agree with it, and a bundle claiming
	// a hundred million rows over a two-row file would otherwise have the
	// verifier allocate for the claim before it had checked anything. The
	// slice grows with what the file actually holds.
	links := make([]chainLink, 0)
	for {
		var row Row
		err := dec.Decode(&row)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			slog.Error("audit.verify.row_decode_failed", slog.String("dir", dir),
				slog.Int("line", len(links)+1), slog.String("err", err.Error()))
			return nil, fmt.Errorf("verify decode events.jsonl line %d: %w", len(links)+1, err)
		}
		report.RowsScanned++
		// An export is one org's ledger. A row from another org inside it is
		// not a filtered gap but a bundle that does not describe itself, and
		// the row's own hash does not cover the org column, so it has to be
		// checked here.
		if row.OrgID != mf.OrgID {
			report.ChainBreaks = append(report.ChainBreaks,
				fmt.Sprintf("row %s belongs to org %s, not the manifest's %s", row.EventID, row.OrgID, mf.OrgID))
		}
		matched, reason, err := checkRowHash(row)
		if err != nil {
			return nil, err
		}
		if matched {
			report.HashMatches++
		} else {
			report.ChainBreaks = append(report.ChainBreaks,
				fmt.Sprintf("row %s %s", row.EventID, reason))
		}
		links = append(links, newChainLink(row))
	}
	// The decoder stops at the last JSON value, so drain whatever trailing
	// bytes remain through the tee before reading the digest; otherwise a
	// trailing newline would change the file's real digest and never reach
	// the hasher.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		slog.Error("audit.verify.events_drain_failed", slog.String("dir", dir), slog.String("err", err.Error()))
		return nil, fmt.Errorf("verify drain events.jsonl: %w", err)
	}
	report.FileSHA256OK = hex.EncodeToString(hasher.Sum(nil)) == mf.FileSHA256
	return links, nil
}
