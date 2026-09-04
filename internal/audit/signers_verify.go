package audit

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// UnknownSigner is one identifier the ledger holds that the valid signer set
// does not, with what the rows under it claim: how many, from which host,
// over what span. This is the signal a leaked key leaves.
type UnknownSigner struct {
	SigningKey  string    `json:"signing_key"`
	SigningHost string    `json:"signing_host"`
	Rows        int       `json:"rows"`
	First       time.Time `json:"first"`
	Last        time.Time `json:"last"`
}

// SignerReport is what `audit signers` prints: every notarization since a
// point in time, sorted into accepted and rejected, with the rejections named.
type SignerReport struct {
	Since                 time.Time       `json:"since"`
	SignerSet             []string        `json:"signer_set"`
	LocalSigner           string          `json:"local_signer,omitempty"`
	RowsScanned           int             `json:"rows_scanned"`
	Accepted              int             `json:"accepted"`
	SignatureVerified     int             `json:"signature_verified"`
	AllowedUnverifiedHere int             `json:"allowed_unverified_here"`
	RejectedUnknownSigner int             `json:"rejected_unknown_signer"`
	SignatureFailed       int             `json:"signature_failed"`
	UnknownSigners        []UnknownSigner `json:"unknown_signers"`
}

// Err names every reason the report fails, and is nil when every notarization
// came from a signer in the set and every signature this host can check
// verified. A caller that reads only the exit code gets the same verdict.
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
	if len(failures) == 0 {
		return nil
	}
	return errors.New(strings.Join(failures, "; "))
}

// errNoSignerSet is returned when verification is asked to run with nothing
// to check against: an empty set would accept nothing, and skipping the check
// would accept everything, and neither is what an operator asked for.
var errNoSignerSet = errors.New("no valid signer set: set AUDIT_VALID_SIGNERS or pass --signers")

// VerifySigners reads every notarization written at or after since and checks
// each against the set. A row whose signing_key is outside the set is
// rejected and reported under its identifier and claimed host. A row whose
// identifier is the local key's is also checked cryptographically; a row
// under another allowed identifier is accepted on the set alone and counted
// as unverified here, because this host does not hold that key's public
// half. The rows stream through the reader role, so the scan reads the ledger
// and writes nothing.
func (r *Reader) VerifySigners(ctx context.Context, set SignerSet, localPub ed25519.PublicKey, since time.Time) (*SignerReport, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("audit reader not configured")
	}
	if !set.Configured() {
		return nil, errNoSignerSet
	}
	report := &SignerReport{
		Since: since, SignerSet: set.Identifiers(), LocalSigner: "",
		RowsScanned: 0, Accepted: 0, SignatureVerified: 0, AllowedUnverifiedHere: 0,
		RejectedUnknownSigner: 0, SignatureFailed: 0, UnknownSigners: []UnknownSigner{},
	}
	if len(localPub) == ed25519.PublicKeySize {
		report.LocalSigner = KeyIdentifier(localPub)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT org_id, notarized_at, merkle_root, signature, signing_key, signing_host
		  FROM audit.notarizations
		 WHERE notarized_at >= $1
		 ORDER BY notarized_at ASC
	`, since)
	if err != nil {
		slog.ErrorContext(ctx, "audit.signers.scan_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("scan audit.notarizations since %s: %w", since.Format(time.RFC3339), err)
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
		if !set.Allows(signingKey) {
			report.RejectedUnknownSigner++
			noteUnknownSigner(unknown, signingKey, signingHost, notarizedAt)
			continue
		}
		report.Accepted++
		if signingKey != report.LocalSigner {
			report.AllowedUnverifiedHere++
			continue
		}
		if ed25519.Verify(localPub, root, signature) {
			report.SignatureVerified++
		} else {
			report.SignatureFailed++
			slog.ErrorContext(ctx, "audit.signers.signature_failed",
				slog.String("org_id", orgID.String()),
				slog.Time("notarized_at", notarizedAt),
				slog.String("signing_key", signingKey),
				slog.String("signing_host", signingHost),
				slog.String("err", "the signature does not verify under the local key"))
		}
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.signers.scan_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("scan audit.notarizations: %w", err)
	}
	for _, entry := range unknown {
		report.UnknownSigners = append(report.UnknownSigners, *entry)
	}
	sort.Slice(report.UnknownSigners, func(i, j int) bool {
		return report.UnknownSigners[i].First.Before(report.UnknownSigners[j].First)
	})
	for _, entry := range report.UnknownSigners {
		slog.ErrorContext(ctx, "audit.signers.unknown_signer",
			slog.String("signing_key", entry.SigningKey),
			slog.String("signing_host", entry.SigningHost),
			slog.Int("rows", entry.Rows),
			slog.Time("first", entry.First),
			slog.Time("last", entry.Last),
			slog.String("err", "notarizations signed outside the valid signer set"))
	}
	return report, nil
}

// noteUnknownSigner folds one rejected row into the per-identifier summary.
// Rows arrive in time order, so the first sighting is the earliest.
func noteUnknownSigner(unknown map[string]*UnknownSigner, signingKey, signingHost string, at time.Time) {
	entry, seen := unknown[signingKey]
	if !seen {
		unknown[signingKey] = &UnknownSigner{
			SigningKey: signingKey, SigningHost: signingHost, Rows: 1, First: at, Last: at,
		}
		return
	}
	entry.Rows++
	entry.Last = at
	if entry.SigningHost == "" {
		entry.SigningHost = signingHost
	}
}
