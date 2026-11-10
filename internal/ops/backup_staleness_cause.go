// Unknown causes distinguish an absent success record, an unreadable record,
// a failed object-store connection, and a guest that cannot observe the
// ledger cluster. An unknown age classifies as stale. An unreadable record
// does not establish whether the mechanism still works, and a failed
// connection does not establish the state of the store. Alarm wording uses
// the cause rather than the detail text.
//
// A remembered reading can supply a known age while preserving the current
// failure cause. The alarm still reports what this run could establish.

package ops

import (
	"errors"

	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// backupStalenessUnknownCause distinguishes the reasons a current reading
// cannot establish a success time.
type backupStalenessUnknownCause int

const (
	// backupStalenessAgeKnown is the zero value for a known age without an
	// unknown cause.
	backupStalenessAgeKnown backupStalenessUnknownCause = iota
	// backupStalenessNeverRecorded means the probe did not find a success
	// marker, a complete export run, or a restorable point.
	backupStalenessNeverRecorded
	// backupStalenessUnreadable means the probe could not read or trust the
	// store, marker, or status, or could not date its timestamp.
	backupStalenessUnreadable
	// backupStalenessStoreUnreachable means the object-store request failed
	// without an API response, including a failed connection or missing
	// response.
	backupStalenessStoreUnreachable
	// backupStalenessClusterUnseen means this guest did not obtain a usable
	// ledger master answer. It cannot establish the cluster's health. An earlier
	// healthy observation can still date the reading. The alarm reports only
	// this guest's inability to observe the cluster, without asserting cluster
	// degradation (TACK-529).
	backupStalenessClusterUnseen
)

// backupStoreReadCause classifies an object-store read failure as a failed
// request or an unreadable response. It recognizes transport failures,
// including refused or unroutable connections, resets, and timeouts, through
// [smithyhttp.RequestSendError]. API errors represent store responses and
// select the unreadable cause.
func backupStoreReadCause(err error) backupStalenessUnknownCause {
	var sendErr *smithyhttp.RequestSendError
	if errors.As(err, &sendErr) {
		return backupStalenessStoreUnreachable
	}
	return backupStalenessUnreadable
}
