package audit

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

func newSignerReport(check SignerCheck, localSigner string) *SignerReport {
	return &SignerReport{
		Since:                  check.Since,
		SignerSet:              check.Set.Identifiers(),
		LocalSigner:            localSigner,
		RowsScanned:            0,
		Accepted:               0,
		SignatureVerified:      0,
		AllowedUnverifiedHere:  0,
		RejectedUnknownSigner:  0,
		SignatureFailed:        0,
		UnknownSigners:         []UnknownSigner{},
		UnverifiedAcknowledged: check.AcknowledgeUnverified,
	}
}

func localSignerMismatch(ctx context.Context, localSigner string, set SignerSet) error {
	wrapped := fmt.Errorf(
		"%w: local %s, set %s",
		ErrLocalKeyNotInSet,
		localSigner,
		strings.Join(set.Identifiers(), ","),
	)
	slog.ErrorContext(
		ctx,
		"audit.signers.configuration_failed",
		slog.String("err", wrapped.Error()),
	)

	return wrapped
}
