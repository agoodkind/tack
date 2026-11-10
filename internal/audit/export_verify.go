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

// VerifyReport summarizes integrity checks for an exported bundle.
type VerifyReport struct {
	BundleDir     string
	RowsScanned   int
	HashMatches   int
	ChainGapCount int
	ChainBreaks   []string
	FileSHA256OK  bool
	SignatureOK   bool
	// ManifestRowCount records the manifest's declared row count. Comparing it with
	// RowsScanned detects count disagreement independently of the signature check.
	ManifestRowCount int
	ManifestSubject  string
	// ManifestSigner records the signer ID from the manifest. VerifiedSigner
	// records the ID of the key that verifies its signature. Signer-set checks use
	// that verified key rather than the manifest's untrusted claim.
	//
	// SignerSetConfigured records whether the caller supplied a signer set.
	// SignerAllowed records whether that set allows the verified key. Without a
	// configured set, the report provides both IDs without enforcing signer policy
	// and records that the set was absent.
	ManifestSigner      string
	VerifiedSigner      string
	SignerSetConfigured bool
	SignerAllowed       bool
}

// Err returns nil when every required integrity check passes. Otherwise, this
// method returns an error identifying each failed check.
//
// Callers can print the report in either case and use this result for scripts,
// scheduled jobs, and release checks that require a failure status for a
// changed bundle.
//
// The result combines these checks:
//   - The file digest, signature, and configured signer policy must pass.
//   - Row hashes, chain links, and the declared row count must agree.
//   - Filtered sequence gaps remain informational.
func (r *VerifyReport) Err() error {
	var failures []string
	if !r.FileSHA256OK {
		failures = append(failures, "the events file digest does not match the manifest")
	}

	if !r.SignatureOK {
		failures = append(failures, "the manifest signature did not verify")
	}

	// A valid signature identifies its signing key, but the configured signer set
	// determines whether that key remains allowed. Reject a key outside the set,
	// including a leaked key (TACK-437).
	if r.SignerSetConfigured && !r.SignerAllowed {
		failures = append(
			failures,
			fmt.Sprintf(
				"the key the signature verified under, %s, is outside the valid signer set",
				r.VerifiedSigner,
			),
		)
	}

	// With a configured signer set, reject a manifest signer ID that differs from
	// the verified key ID, regardless of set membership. Older bundles used a
	// different key identifier and deliberately verify without a configured set.
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

	// Time, actor, or entity filters can legitimately omit sequence numbers. Count
	// those gaps without failing verification; rejecting them would reject valid
	// filtered exports. Row hashes and adjacent previous-hash links detect covered
	// changes. The report preserves the gap count for inspection.
	//
	// A mismatch between HashMatches and RowsScanned also reports rows that failed
	// their stored hash check, even if another check did not fail.
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

	// Compare the file's scanned row count with the signed manifest's declared
	// count. A changed manifest or an incomplete export can make those values
	// disagree, and the bundle must fail either way.
	if r.ManifestRowCount != r.RowsScanned {
		failures = append(
			failures,
			fmt.Sprintf(
				"the manifest declares %d rows but the events file holds %d",
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

// chainLink stores the fixed-width coordinates and two hashes that the chain
// pass needs after row scanning. Earlier verification retained full rows,
// including parsed context, delta, and extra payloads. A 420,000-row production
// bundle exhausted the app container's memory limit and terminated the verifier
// (TACK-463). The second pass now reads these smaller records rather than
// retaining total bundle payloads.
type chainLink struct {
	Shard    int16
	Seq      int64
	EventID  uuid.UUID
	PrevHash [sha256.Size]byte
	RowHash  [sha256.Size]byte
}

// VerifyBundle checks a bundle offline using the supplied public key. It
// recomputes row hashes, verifies prev_hash links within each (org, shard)
// sequence, and checks the manifest signature.
//
// The signed manifest selects its rows file rather than relying on a fixed
// filename. A different export's rows cannot substitute for that signed
// selection. Older manifests omit the filename and use the legacy fixed name.
//
// Verification uses these stages:
//  1. Read the manifest and its selected rows file.
//  2. Decode and hash each row, retaining only its fixed-width chain link.
//  3. Sort the retained links by shard and sequence and check their links.
//
// Peak retained row state grows with the number of fixed-width links rather
// than total bundle payload bytes.
func VerifyBundle(dir string, pub ed25519.PublicKey) (*VerifyReport, error) {
	return VerifyBundleWithSigners(dir, pub, SignerSet{ordered: nil, members: nil})
}

// VerifyBundleWithSigners performs offline bundle verification with the
// supplied public key and signer set.
//
// Signer policy applies only when signers configures a set (TACK-437). The
// cryptographic check identifies the verified key independently of the
// manifest's claimed signer.
//
// The report distinguishes these policy failures:
//   - The verified key does not belong to the configured set.
//   - The manifest claims a signer other than the verified key.
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

// scanBundleRows reads the manifest's selected rows file once. It computes the
// file digest, decodes and verifies one row at a time, and retains each row's
// chain link. Each decoded full row goes out of scope after that iteration.
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
	// Reject keys that the row's type does not declare. Hash recomputation uses the
	// decoded typed fields, and silently discarding an unknown key would permit a
	// planted field to verify.
	dec.DisallowUnknownFields()

	// Grow the link slice from decoded rows rather than the manifest's claimed
	// count. The manifest count remains unverified until the signature and file
	// checks agree. Preallocating for a claim of one hundred million rows in a
	// two-row file would allocate for untrusted input before verification.
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
		// An export contains one organization's ledger. Compare each row's organization
		// with the manifest's organization. A row can have a valid hash and chain for a
		// different organization and still violate this bundle boundary.
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

	// The JSON decoder stops after parsing values. Read any remaining trailing
	// bytes through the digest reader before comparing hashes. A trailing newline
	// must contribute to the file digest too.
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
