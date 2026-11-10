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

// SignerCheck supplies the inputs for a signer verification run.
type SignerCheck struct {
	Set      SignerSet
	LocalPub ed25519.PublicKey
	Since    time.Time
	// AcknowledgeUnverified accepts rows with allowed identifiers when this
	// host lacks their keys, as during key rotation or with a second signing
	// guest.
	AcknowledgeUnverified bool
}

// VerifySigners reads notarizations at or after check.Since and checks their
// signing identifiers against check.Set.
//
// The query uses the ledger reader and does not write audit rows. The report
// includes rejected identifiers and their claimed hosts.
//
//   - An identifier outside the set causes rejection.
//   - A row with the local identifier must pass signature verification.
//   - A row with another allowed identifier counts as unverified here and
//     fails the report unless check.AcknowledgeUnverified accepts it.
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
		return nil, localSignerMismatch(ctx, localSigner, check.Set)
	}

	report := newSignerReport(check, localSigner)
	rows, err := r.pool.Rows(
		r.pool.unsanitized.Query(
			ctx,
			`
		SELECT org_id, notarized_at, merkle_root, signature, signing_key, signing_host
		  FROM audit.notarizations
		 WHERE notarized_at >= $1
	`,
			check.Since,
		),
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit.signers.scan_failed", slog.String("err", err.Error()))

		return nil, fmt.Errorf(
			"scan audit.notarizations since %s: %w",
			check.Since.Format(time.RFC3339),
			err,
		)
	}
	defer rows.Close()
	unknown := map[string]*UnknownSigner{}

	for rows.Next() {
		var orgID uuid.UUID
		var notarizedAt time.Time
		var root, signature []byte
		var signingKey, signingHost string
		if err := rows.Scan(
			&orgID,
			&notarizedAt,
			&root,
			&signature,
			&signingKey,
			&signingHost,
		); err != nil {
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
		slog.ErrorContext(
			ctx,
			"audit.signers.signature_failed",
			slog.String("org_id", orgID.String()),
			slog.Time("notarized_at", notarizedAt),
			slog.String("signing_key", signingKey),
			slog.String("signing_host", signingHost),
			slog.String("err", "the signature does not verify under the local key"),
		)
	}

	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.signers.scan_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("scan audit.notarizations: %w", err)
	}

	report.UnknownSigners = summarizeUnknownSigners(ctx, unknown)

	return report, nil
}

// noteUnknownSigner adds a rejected row to its identifier's summary. The
// summary counts rows, tracks the earliest and latest times, and lists every
// claimed host.
func noteUnknownSigner(
	unknown map[string]*UnknownSigner,
	signingKey string,
	signingHost string,
	at time.Time,
) {
	entry, seen := unknown[signingKey]
	if !seen {
		unknown[signingKey] = &UnknownSigner{
			SigningKey:   signingKey,
			SigningHosts: []string{signingHost},
			Rows:         1,
			First:        at,
			Last:         at,
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

// summarizeUnknownSigners sorts summaries by first sighting and logs one Error
// line per identifier.
func summarizeUnknownSigners(
	ctx context.Context,
	unknown map[string]*UnknownSigner,
) []UnknownSigner {
	out := make([]UnknownSigner, 0, len(unknown))
	for _, entry := range unknown {
		sort.Strings(entry.SigningHosts)
		out = append(out, *entry)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].First.Before(out[j].First) })

	for _, entry := range out {
		slog.ErrorContext(
			ctx,
			"audit.signers.unknown_signer",
			slog.String("signing_key", entry.SigningKey),
			slog.String("signing_hosts", strings.Join(entry.SigningHosts, ",")),
			slog.Int("rows", entry.Rows),
			slog.Time("first", entry.First),
			slog.Time("last", entry.Last),
			slog.String("err", "notarizations signed outside the valid signer set"),
		)
	}

	return out
}
