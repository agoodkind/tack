package foundationdb

import (
	"encoding/json"
	"errors"
	"time"
)

// retireAttemptValueLimit is the largest value, in bytes, that
// FoundationDB stores. A larger value fails its commit with error 2103.
const retireAttemptValueLimit = 100_000

// errRetireObservationTooLarge signals that an attempt record with its
// observation time exceeds retireAttemptValueLimit. JSON encoding can
// increase the size of a stored record. observeRetireAttempt returns
// this error, and recoveredRetireEvent treats the record as invalid.
var errRetireObservationTooLarge = errors.New(
	"the retirement attempt record with its observation time exceeds the FoundationDB value limit",
)

// observeRetireAttempt reports whether record is active at now.
// It returns a value to store when record needs a first observation.
// A deadline at or before now means record has expired.
// A later deadline with a zero or future ObservedAt keeps record active.
// The function returns record with ObservedAt set to now in that case.
// It returns errRetireObservationTooLarge if that value exceeds
// retireAttemptValueLimit. A record with a later deadline expires at
// ObservedAt plus retireAttemptLease. Any other record stays active
// without a write. The first observation cannot precede the attempt
// save. A far-future deadline cannot stall retirement beyond the lease.
// A recovery clock error can move the expiry.
func observeRetireAttempt(record retireAttemptRecord, now time.Time) (bool, []byte, error) {
	if !record.Deadline.After(now) {
		return false, nil, nil
	}

	if record.ObservedAt.IsZero() || record.ObservedAt.After(now) {
		record.ObservedAt = now.UTC()
		observed, err := json.Marshal(record)
		if err != nil {
			return true, nil, sessionStepError{
				operation: "encode retirement attempt observation",
				err:       err,
			}
		}

		if len(observed) > retireAttemptValueLimit {
			return true, nil, errRetireObservationTooLarge
		}

		return true, observed, nil
	}

	return now.Before(record.ObservedAt.Add(retireAttemptLease)), nil, nil
}
