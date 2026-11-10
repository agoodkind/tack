package audit

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// UnknownSigner summarizes ledger rows with an identifier outside the valid
// signer set. The summary includes the row count, claimed hosts, and time span
// for investigation of a suspected leaked key. An unknown identifier alone
// does not prove that a key leaked.
type UnknownSigner struct {
	SigningKey   string    `json:"signing_key"`
	SigningHosts []string  `json:"signing_hosts"`
	Rows         int       `json:"rows"`
	First        time.Time `json:"first"`
	Last         time.Time `json:"last"`
}

// SignerReport provides the audit signers output for notarizations since a
// specified time. It counts accepted and rejected rows and identifies the
// rejection reasons.
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
	// UnverifiedAcknowledged records the operator's acceptance of rows under
	// allowed identifiers when this host lacks their keys. These rows fail the
	// report without acknowledgment. Anyone can write an identifier; only
	// signature verification proves that the signer used the key.
	UnverifiedAcknowledged bool `json:"unverified_acknowledged"`
}

// Err returns each reason the signer report fails.
//
// The audit signers command uses this verdict for its exit result after
// printing the report.
//
//   - An identifier outside the valid signer set fails the report.
//   - A signature that fails verification with the local key fails the report.
//   - A row under another allowed identifier fails the report unless the
//     operator acknowledges the unavailable signature verification.
//
// Err returns nil when the report does not contain any of these failures.
func (r *SignerReport) Err() error {
	var failures []string
	if r.RejectedUnknownSigner > 0 {
		ids := make([]string, 0, len(r.UnknownSigners))
		for _, unknown := range r.UnknownSigners {
			ids = append(ids, unknown.SigningKey)
		}

		failures = append(
			failures,
			fmt.Sprintf(
				"%d notarization(s) signed outside the valid signer set by %s",
				r.RejectedUnknownSigner,
				strings.Join(ids, ", "),
			),
		)
	}

	if r.SignatureFailed > 0 {
		failures = append(
			failures,
			fmt.Sprintf(
				"%d notarization(s) carry a signature the local key does not verify",
				r.SignatureFailed,
			),
		)
	}

	if r.AllowedUnverifiedHere > 0 && !r.UnverifiedAcknowledged {
		failures = append(
			failures,
			fmt.Sprintf(
				"%d notarization(s) under allowed identifiers this host holds no key for; "+
					"pass --allow-unverified to accept them on the set alone",
				r.AllowedUnverifiedHere,
			),
		)
	}

	if len(failures) == 0 {
		return nil
	}

	return errors.New(strings.Join(failures, "; "))
}

// ErrNoSignerSet indicates that verification lacks a valid signer set.
// An empty set cannot accept any identifier. Skipping the membership check
// would accept every identifier. Neither case provides the requested check
// against an operator's valid signer set.
var ErrNoSignerSet = errors.New("no valid signer set: set AUDIT_VALID_SIGNERS or pass --signers")

// ErrNoLocalKey indicates that verification lacks a local signing key.
// Matching identifiers without checking a signature proves only that the
// strings match and cannot justify successful signature verification.
var ErrNoLocalKey = errors.New("no local signing key: pass --pub or set AUDIT_SIGNING_KEY_PATH")

// ErrLocalKeyNotInSet indicates that the valid signer set excludes the local
// key. Every allowed row would then lack local signature verification, and
// acknowledgment could accept all rows without verifying any signature.
// Rotation overlap and a second signing guest are the intended acknowledgment
// cases. Both cases retain the local key in the valid signer set.
var ErrLocalKeyNotInSet = errors.New("the local signing key is not in the valid signer set")
