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
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// VerifyReport summarizes export bundle integrity checks.
type VerifyReport struct {
	BundleDir     string
	RowsScanned   int
	HashMatches   int
	ChainGapCount int
	ChainBreaks   []string
	FileSHA256OK  bool
	SignatureOK   bool
	// ManifestRowCount records the claimed count for comparison with RowsScanned.
	ManifestRowCount int
	ManifestSubject  string
	// ManifestSigner records the claimed signer; VerifiedSigner records the
	// verification key's identifier. A configured set checks the verified key
	// and requires both identifiers to match. Without a set, the report records
	// these identifiers without judging membership or matching their spelling.
	ManifestSigner      string
	VerifiedSigner      string
	SignerSetConfigured bool
	SignerAllowed       bool
}

// Err returns all failed integrity checks or nil for a verified bundle.
// Callers must use this verdict as well as displaying the detailed report.
func (r *VerifyReport) Err() error {
	var failures []string
	if !r.FileSHA256OK {
		failures = append(failures, "the events file digest does not match the manifest")
	}

	if !r.SignatureOK {
		failures = append(failures, "the manifest signature did not verify")
	}

	// A valid signature does not authorize a key outside the configured set (TACK-437).
	if r.SignerSetConfigured && !r.SignerAllowed {
		failures = append(
			failures,
			fmt.Sprintf(
				"the key the signature verified under, %s, is outside the valid signer set",
				r.VerifiedSigner,
			),
		)
	}

	// Require the claimed identifier to match the verification key when a set
	// is configured. Older bundles use different identifiers without a set.
	if r.SignerSetConfigured && r.ManifestSigner != r.VerifiedSigner {
		failures = append(
			failures,
			fmt.Sprintf(
				"the manifest names signer %s but its signature verified under %s",
				r.ManifestSigner,
				r.VerifiedSigner,
			),
		)
	}

	if len(r.ChainBreaks) > 0 {
		failures = append(
			failures,
			fmt.Sprintf("%d chain break(s), first: %s", len(r.ChainBreaks), r.ChainBreaks[0]),
		)
	}

	// Filtered exports can omit sequence numbers legitimately. Report gaps
	// without rejecting them; reject hash mismatches and broken links instead.
	if r.HashMatches != r.RowsScanned {
		failures = append(
			failures,
			fmt.Sprintf(
				"%d of %d rows failed their hash check",
				r.RowsScanned-r.HashMatches,
				r.RowsScanned,
			),
		)
	}

	// Check the actual row count even when the signature and digest verify.
	if r.ManifestRowCount != r.RowsScanned {
		failures = append(
			failures,
			fmt.Sprintf(
				"the manifest declares %d rows but the events file contains %d",
				r.ManifestRowCount,
				r.RowsScanned,
			),
		)
	}

	if len(failures) == 0 {
		return nil
	}

	return fmt.Errorf(
		"bundle %s failed verification: %s",
		r.BundleDir,
		strings.Join(failures, "; "),
	)
}

// chainLink retains fixed-width chain coordinates and hashes after each row
// check. The link pass does not retain variable-size context, delta, or extra
// payloads from the full Row (TACK-463).
type chainLink struct {
	Shard    int16
	Seq      int64
	EventID  uuid.UUID
	PrevHash [sha256.Size]byte
	RowHash  [sha256.Size]byte
}

// VerifyBundle checks row hashes, per-shard chain links, the file digest, and
// the manifest signature offline. The signed manifest selects the rows file.
// Verification decodes one row at a time and retains only fixed-width links.
func VerifyBundle(dir string, pub ed25519.PublicKey) (*VerifyReport, error) {
	return VerifyBundleWithSigners(dir, pub, SignerSet{ordered: nil, members: nil})
}

// VerifyBundleWithSigners also rejects verification keys outside a configured
// signer set and claimed identifiers that differ from the verified key
// (TACK-437).
func VerifyBundleWithSigners(
	dir string,
	pub ed25519.PublicKey,
	signers SignerSet,
) (*VerifyReport, error) {
	report := &VerifyReport{
		BundleDir:           dir,
		RowsScanned:         0,
		HashMatches:         0,
		ChainGapCount:       0,
		ChainBreaks:         nil,
		FileSHA256OK:        false,
		SignatureOK:         false,
		ManifestRowCount:    0,
		ManifestSubject:     "",
		ManifestSigner:      "",
		VerifiedSigner:      "",
		SignerSetConfigured: signers.Configured(),
		SignerAllowed:       false,
	}
	if len(pub) == ed25519.PublicKeySize {
		report.VerifiedSigner = KeyIdentifier(pub)
		report.SignerAllowed = signers.Allows(report.VerifiedSigner)
	}

	mf, err := readExportManifest(dir)
	if err != nil {
		return nil, err
	}

	report.ManifestSubject = mf.ExportID.String()
	report.ManifestRowCount = mf.RowCount
	report.ManifestSigner = mf.SignatureBy
	report.SignatureOK = manifestSignatureOK(mf, pub)

	eventsName, err := bundleEventsFileName(mf)
	if err != nil {
		slog.Error(
			"audit.verify.events_name_rejected",
			slog.String("dir", dir),
			slog.String("err", err.Error()),
		)

		return report, err
	}

	links, err := scanBundleRows(dir, eventsName, report, mf)
	if err != nil {
		return report, err
	}

	verifyChainLinks(report, links)

	return report, nil
}

// scanBundleRows computes the file digest, checks each decoded row, and
// retains only chain links rather than complete rows.
func scanBundleRows(
	dir string,
	eventsName string,
	report *VerifyReport,
	mf ExportManifest,
) ([]chainLink, error) {
	path := filepath.Join(dir, eventsName)
	file, err := openBundleRowsFile(path)
	if err != nil {
		slog.Error(
			"audit.verify.events_read_failed",
			slog.String("dir", dir),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("verify jsonl: %w", err)
	}

	defer func() { _ = file.Close() }()

	hasher := sha256.New()
	reader := bufio.NewReaderSize(io.TeeReader(file, hasher), 1<<20)
	dec := json.NewDecoder(reader)
	// Reject unknown keys instead of hiding injected fields during re-encoding.
	dec.DisallowUnknownFields()

	// Allocate from actual rows, never an untrusted manifest count.
	links := make([]chainLink, 0)

	for {
		var row Row
		err := dec.Decode(&row)
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			slog.Error(
				"audit.verify.row_decode_failed",
				slog.String("dir", dir),
				slog.Int("line", len(links)+1),
				slog.String("err", err.Error()),
			)

			return nil, fmt.Errorf("verify decode %s line %d: %w", eventsName, len(links)+1, err)
		}

		report.RowsScanned++
		// Require each row's org column to match the manifest independently
		// of its hash; a foreign row can have a valid hash for its own org.
		if row.OrgID != mf.OrgID {
			report.ChainBreaks = append(
				report.ChainBreaks,
				fmt.Sprintf(
					"row %s belongs to org %s, not the manifest's %s",
					row.EventID,
					row.OrgID,
					mf.OrgID,
				),
			)
		}

		matched, reason, err := checkRowHash(row)
		if err != nil {
			return nil, err
		}

		if manifestReason := manifestViolation(row); matched && manifestReason != "" {
			matched, reason = false, manifestReason
		}

		if matched {
			report.HashMatches++
		} else {
			report.ChainBreaks = append(
				report.ChainBreaks,
				fmt.Sprintf("row %s %s", row.EventID, reason),
			)
		}

		links = append(links, newChainLink(row))
	}

	// Include trailing bytes such as newlines in the file digest after JSON decoding.
	if _, err := io.Copy(io.Discard, reader); err != nil {
		slog.Error(
			"audit.verify.events_drain_failed",
			slog.String("dir", dir),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("verify drain %s: %w", eventsName, err)
	}

	report.FileSHA256OK = hex.EncodeToString(hasher.Sum(nil)) == mf.FileSHA256

	return links, nil
}
