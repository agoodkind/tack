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
	localSigner := KeyIdentifier(check.LocalPub)
	if !check.Set.Allows(localSigner) {
		return nil, fmt.Errorf("%w: local %s, set %s", ErrLocalKeyNotInSet, localSigner, strings.Join(check.Set.Identifiers(), ","))
	}
	report := &SignerReport{
		Since: check.Since, SignerSet: check.Set.Identifiers(), LocalSigner: localSigner,
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
