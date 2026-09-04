package audit

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// UnknownSigner is one identifier the ledger holds that the valid signer set
// does not, with what the rows under it claim: how many, from which hosts,
// over what span. This is the signal a leaked key leaves.
type UnknownSigner struct {
	SigningKey   string    `json:"signing_key"`
	SigningHosts []string  `json:"signing_hosts"`
	Rows         int       `json:"rows"`
	First        time.Time `json:"first"`
	Last         time.Time `json:"last"`
}

// SignerReport is what `audit signers` prints: every notarization since a
// point in time, sorted into accepted and rejected, with the rejections named.
type SignerReport struct {
	Since                 time.Time       `json:"since"`
	SignerSet             []string        `json:"signer_set"`
	LocalSigner           string          `json:"local_signer"`
	RowsScanned           int             `json:"rows_scanned"`
	Accepted              int             `json:"accepted"`
	SignatureVerified     int             `json:"signature_verified"`
	AllowedUnverifiedHere int             `json:"allowed_unverified_here"`
	RejectedUnknownSigner int             `json:"rejected_unknown_signer"`
	SignatureFailed       int             `json:"signature_failed"`
	UnknownSigners        []UnknownSigner `json:"unknown_signers"`
	// UnverifiedAcknowledged records that the operator accepted rows under
	// allowed identifiers this host holds no key for. Without it those rows
	// fail the report, because an identifier alone is a label anyone can
	// write and only a signature proves the key.
	UnverifiedAcknowledged bool `json:"unverified_acknowledged"`
}

// Err names every reason the report fails, and is nil when every notarization
// came from a signer in the set and every signature was verified, or the
// unverified ones were acknowledged. A caller that reads only the exit code
// gets the same verdict.
func (r *SignerReport) Err() error {
	var failures []string
	if r.RejectedUnknownSigner > 0 {
		ids := make([]string, 0, len(r.UnknownSigners))
		for _, unknown := range r.UnknownSigners {
			ids = append(ids, unknown.SigningKey)
		}
		failures = append(failures, fmt.Sprintf("%d notarization(s) signed outside the valid signer set by %s",
			r.RejectedUnknownSigner, strings.Join(ids, ", ")))
	}
	if r.SignatureFailed > 0 {
		failures = append(failures, fmt.Sprintf("%d notarization(s) carry a signature the local key does not verify",
			r.SignatureFailed))
	}
	if r.AllowedUnverifiedHere > 0 && !r.UnverifiedAcknowledged {
		failures = append(failures, fmt.Sprintf("%d notarization(s) under allowed identifiers this host holds no key for; "+
			"pass --allow-unverified to accept them on the set alone", r.AllowedUnverifiedHere))
	}
	if len(failures) == 0 {
		return nil
	}
	return errors.New(strings.Join(failures, "; "))
}

// ErrNoSignerSet is returned when verification is asked to run with nothing
// to check against: an empty set would accept nothing, and skipping the check
// would accept everything, and neither is what an operator asked for.
var ErrNoSignerSet = errors.New("no valid signer set: set AUDIT_VALID_SIGNERS or pass --signers")

// ErrNoLocalKey is returned when no signing key is available to check
// signatures with: a run that matched identifiers and verified no signature
// would read as a pass while proving only that strings matched.
var ErrNoLocalKey = errors.New("no local signing key: pass --pub or set AUDIT_SIGNING_KEY_PATH")

// SignerCheck is what one verification run needs beyond the reader.
type SignerCheck struct {
	Set      SignerSet
	LocalPub ed25519.PublicKey
	Since    time.Time
	// AcknowledgeUnverified accepts rows under allowed identifiers this host
	// holds no key for, which is a rotation overlap or a second signing guest.
	AcknowledgeUnverified bool
}

// VerifySigners reads every notarization written at or after Since and checks
// each against the set. A row whose signing_key is outside the set is
// rejected and reported under its identifier and the hosts it claimed. A row
// whose identifier is the local key's is also checked cryptographically; a
// row under another allowed identifier is counted as unverified here and
// fails the report unless acknowledged. The rows stream through the reader
// role, so the scan reads the ledger and writes nothing.
func (r *Reader) VerifySigners(ctx context.Context, check SignerCheck) (*SignerReport, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("audit reader not configured")
	}
	if !check.Set.Configured() {
		return nil, ErrNoSignerSet
	}
	if len(check.LocalPub) != ed25519.PublicKeySize {
		return nil, ErrNoLocalKey
	}
	report := &SignerReport{
		Since: check.Since, SignerSet: check.Set.Identifiers(), LocalSigner: KeyIdentifier(check.LocalPub),
		RowsScanned: 0, Accepted: 0, SignatureVerified: 0, AllowedUnverifiedHere: 0,
		RejectedUnknownSigner: 0, SignatureFailed: 0, UnknownSigners: []UnknownSigner{},
		UnverifiedAcknowledged: check.AcknowledgeUnverified,
	}
	rows, err := r.pool.Query(ctx, `
		SELECT org_id, notarized_at, merkle_root, signature, signing_key, signing_host
		  FROM audit.notarizations
		 WHERE notarized_at >= $1
	`, check.Since)
	if err != nil {
		slog.ErrorContext(ctx, "audit.signers.scan_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("scan audit.notarizations since %s: %w", check.Since.Format(time.RFC3339), err)
	}
	defer rows.Close()
	unknown := map[string]*UnknownSigner{}
	for rows.Next() {
		var orgID uuid.UUID
		var notarizedAt time.Time
		var root, signature []byte
		var signingKey, signingHost string
		if err := rows.Scan(&orgID, &notarizedAt, &root, &signature, &signingKey, &signingHost); err != nil {
			slog.ErrorContext(ctx, "audit.signers.scan_row_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("read a notarization row: %w", err)
		}
		report.RowsScanned++
		if !check.Set.Allows(signingKey) {
			report.RejectedUnknownSigner++
			noteUnknownSigner(unknown, signingKey, signingHost, notarizedAt)
			continue
		}
		report.Accepted++
		if signingKey != report.LocalSigner {
			report.AllowedUnverifiedHere++
			continue
		}
		if ed25519.Verify(check.LocalPub, root, signature) {
			report.SignatureVerified++
			continue
		}
		report.SignatureFailed++
		slog.ErrorContext(ctx, "audit.signers.signature_failed",
			slog.String("org_id", orgID.String()),
			slog.Time("notarized_at", notarizedAt),
			slog.String("signing_key", signingKey),
			slog.String("signing_host", signingHost),
			slog.String("err", "the signature does not verify under the local key"))
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.signers.scan_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("scan audit.notarizations: %w", err)
	}
	report.UnknownSigners = summarizeUnknownSigners(ctx, unknown)
	return report, nil
}

// noteUnknownSigner folds one rejected row into the per-identifier summary:
// the row count, the earliest and latest sighting, and every host claimed.
func noteUnknownSigner(unknown map[string]*UnknownSigner, signingKey, signingHost string, at time.Time) {
	entry, seen := unknown[signingKey]
	if !seen {
		unknown[signingKey] = &UnknownSigner{
			SigningKey: signingKey, SigningHosts: []string{signingHost}, Rows: 1, First: at, Last: at,
		}
		return
	}
	entry.Rows++
	if at.Before(entry.First) {
		entry.First = at
	}
	if at.After(entry.Last) {
		entry.Last = at
	}
	if !slices.Contains(entry.SigningHosts, signingHost) {
		entry.SigningHosts = append(entry.SigningHosts, signingHost)
	}
}

// summarizeUnknownSigners orders the summaries by first sighting and logs
// each at error level, one line per identifier rather than one per row.
func summarizeUnknownSigners(ctx context.Context, unknown map[string]*UnknownSigner) []UnknownSigner {
	out := make([]UnknownSigner, 0, len(unknown))
	for _, entry := range unknown {
		sort.Strings(entry.SigningHosts)
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].First.Before(out[j].First) })
	for _, entry := range out {
		slog.ErrorContext(ctx, "audit.signers.unknown_signer",
			slog.String("signing_key", entry.SigningKey),
			slog.String("signing_hosts", strings.Join(entry.SigningHosts, ",")),
			slog.Int("rows", entry.Rows),
			slog.Time("first", entry.First),
			slog.Time("last", entry.Last),
			slog.String("err", "notarizations signed outside the valid signer set"))
	}
	return out
}
