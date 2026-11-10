package audit

import (
	"errors"
	"fmt"
	"strings"
	"time"
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

// ErrLocalKeyNotInSet is returned when the local key is not one the set
// accepts: every allowed row would then be unverifiable here, and an
// acknowledgement would clear them all having verified nothing. The cases
// the acknowledgement exists for, a rotation overlap or a second signing
// guest, both keep the local key in the set.
var ErrLocalKeyNotInSet = errors.New("the local signing key is not in the valid signer set")
