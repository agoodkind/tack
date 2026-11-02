package foundationdb

import (
	"encoding/json"
	"time"
)

// observeRetireAttempt reports whether record is active at now. It returns
// the value to store when the record needs a first observation. A deadline at
// or before now means the record has expired. A later deadline with a zero
// or future ObservedAt keeps the record active. The function then returns
// the record with ObservedAt set to now. A record with a later deadline
// expires at ObservedAt plus retireAttemptLease. Any other record stays
// active without a write. The first observation cannot precede the attempt
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
			return true, nil, sessionStepError{operation: "encode retirement attempt observation", err: err}
		}
		return true, observed, nil
	}
	return now.Before(record.ObservedAt.Add(retireAttemptLease)), nil, nil
}
