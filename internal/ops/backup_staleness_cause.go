// backup_staleness_cause.go names why a backup mechanism's age is unknown. An
// unknown age is always stale, but it covers situations that support
// different claims: a mechanism that never recorded a success has produced
// nothing, while one whose record could not be read may be working, and a
// record the object store never answered for says the store is the fault. The
// alarm words key on this value rather than on the detail text.

package ops

import (
	"errors"

	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// backupStalenessUnknownCause is the kind of unknown an age-unknown metric
// carries.
type backupStalenessUnknownCause int

const (
	// backupStalenessAgeKnown is the zero value: the age is known and no
	// cause applies.
	backupStalenessAgeKnown backupStalenessUnknownCause = iota
	// backupStalenessNeverRecorded means the store holds no success at all:
	// no marker, no complete export run, no restorable point.
	backupStalenessNeverRecorded
	// backupStalenessUnreadable means a reading could not be taken or
	// trusted: the store, marker, or status could not be read, or the
	// timestamp it carried could not be dated.
	backupStalenessUnreadable
	// backupStalenessStoreUnreachable means the object store holding the
	// record sent no answer at all: the request never reached it or no
	// response came back.
	backupStalenessStoreUnreachable
)

// backupStoreReadCause classifies a failed object-store read. The S3 client
// wraps every transport failure (a refused or unroutable connection, a reset,
// a timeout) in a [smithyhttp.RequestSendError] and returns an answer the
// store sent as an API error, so the wrapper alone tells an unreachable store
// from one that answered with a refusal.
func backupStoreReadCause(err error) backupStalenessUnknownCause {
	var sendErr *smithyhttp.RequestSendError
	if errors.As(err, &sendErr) {
		return backupStalenessStoreUnreachable
	}
	return backupStalenessUnreadable
}
