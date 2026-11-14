// Backup staleness causes distinguish absent success records, unreadable
// records, object-store transport failures, and failed ledger probes. Unknown
// ages count as stale, but each cause supports a different alarm message.
// A known-age reading can retain a cause when it uses a previous observation
// instead of a fresh probe.

package ops

import (
	"errors"

	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// backupStalenessUnknownCause classifies an unavailable or historical reading.
type backupStalenessUnknownCause int

const (
	// backupStalenessAgeKnown indicates a known age without a failure cause.
	backupStalenessAgeKnown backupStalenessUnknownCause = iota
	// backupStalenessNeverRecorded indicates that the store does not contain
	// a success marker, complete export run, or restorable point.
	backupStalenessNeverRecorded
	// backupStalenessUnreadable indicates that a read failed or its record,
	// status, or timestamp could not establish a trustworthy age.
	backupStalenessUnreadable
	// backupStalenessStoreUnreachable indicates an object-store transport
	// failure without a response.
	backupStalenessStoreUnreachable
	// backupStalenessClusterUnseen indicates that every ledger master probe
	// failed. A historical reading may date the last healthy observation, but
	// the alarm cannot establish that the cluster is unhealthy (TACK-529).
	backupStalenessClusterUnseen
)

// backupStoreReadCause distinguishes transport failures from API refusals.
// The S3 client wraps connection refusals, routing failures, resets, and
// timeouts in smithyhttp.RequestSendError. An API error instead indicates
// that the object store answered the request.
func backupStoreReadCause(err error) backupStalenessUnknownCause {
	var sendErr *smithyhttp.RequestSendError
	if errors.As(err, &sendErr) {
		return backupStalenessStoreUnreachable
	}

	return backupStalenessUnreadable
}
