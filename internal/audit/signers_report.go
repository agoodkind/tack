package audit

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// UnknownSigner summarizes ledger rows with an identifier outside the valid
// signer set. It records the row count, claimed hosts, and time span.
type UnknownSigner struct {
	SigningKey   string    `json:"signing_key"`
	SigningHosts []string  `json:"signing_hosts"`
	Rows         int       `json:"rows"`
	First        time.Time `json:"first"`
	Last         time.Time `json:"last"`
}

// SignerReport summarizes accepted and rejected notarizations since Since.
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
	// allowed identifiers without a local verification key. An identifier
	// alone does not prove that the signer used the key.
	UnverifiedAcknowledged bool `json:"unverified_acknowledged"`
}

// Err reports unknown signers, invalid local signatures, and unacknowledged
// allowed signers without a local verification key. It returns nil only when
// all these checks pass.
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
				"the local key does not verify the signature on %d notarization(s)",
				r.SignatureFailed,
			),
		)
	}

	if r.AllowedUnverifiedHere > 0 && !r.UnverifiedAcknowledged {
		failures = append(
			failures,
			fmt.Sprintf(
				"this host cannot verify %d notarization(s) under allowed identifiers without their keys; "+
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

// ErrNoSignerSet reports that verification requires a nonempty valid signer
// set.
var ErrNoSignerSet = errors.New("no valid signer set: set AUDIT_VALID_SIGNERS or pass --signers")

// ErrNoLocalKey reports that verification requires a local signing key.
// Matching identifiers alone does not verify signatures.
var ErrNoLocalKey = errors.New("no local signing key: pass --pub or set AUDIT_SIGNING_KEY_PATH")

// ErrLocalKeyNotInSet reports that the set does not accept the local key.
// Rotation overlap and additional signing guests still require the local key
// in the set; acknowledgement must not accept every row without verification.
var ErrLocalKeyNotInSet = errors.New("the local signing key is not in the valid signer set")
