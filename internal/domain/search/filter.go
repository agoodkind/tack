package search

import (
	"errors"
	"slices"
	"strconv"
)

const (
	// MaxAccessKeys bounds the opaque caller keys one query may send.
	MaxAccessKeys = 64
	// MaxAccessValueBytes bounds one opaque access version or key.
	MaxAccessValueBytes = 256
)

// ErrInvalidAccessFilter means the caller access values cannot filter a query.
var ErrInvalidAccessFilter = errors.New("search access filter is invalid")

// invalidFilterError reports one rejected access filter property.
type invalidFilterError struct{ reason string }

func (e invalidFilterError) Error() string { return ErrInvalidAccessFilter.Error() + ": " + e.reason }
func (e invalidFilterError) Unwrap() error { return ErrInvalidAccessFilter }

// AccessFilter is the opaque caller access that OpenSearch applies before
// ranking. Search code compares the values and never decodes them.
type AccessFilter struct {
	Version string
	Keys    []string
}

// ValidateAccessFilter rejects an empty, duplicate, unsorted, or oversized
// access filter.
func ValidateAccessFilter(filter AccessFilter) error {
	valueBound := strconv.Itoa(MaxAccessValueBytes)
	if filter.Version == "" || len(filter.Version) > MaxAccessValueBytes {
		return invalidFilterError{reason: "the version must contain between 1 and " + valueBound + " bytes"}
	}
	if len(filter.Keys) == 0 || len(filter.Keys) > MaxAccessKeys {
		return invalidFilterError{reason: "the filter must contain between 1 and " + strconv.Itoa(MaxAccessKeys) + " keys"}
	}
	if !slices.IsSorted(filter.Keys) || hasDuplicate(filter.Keys) {
		return invalidFilterError{reason: "keys must be sorted and unique"}
	}
	for _, key := range filter.Keys {
		if key == "" || len(key) > MaxAccessValueBytes {
			return invalidFilterError{reason: "each key must contain between 1 and " + valueBound + " bytes"}
		}
	}
	return nil
}

// Permits reports whether any current resource key equals a caller key. It
// binary-searches f.Keys, which Validate requires to be sorted.
// resourceKeys can be in any order.
func (f AccessFilter) Permits(resourceKeys []string) bool {
	for _, key := range resourceKeys {
		if _, found := slices.BinarySearch(f.Keys, key); found {
			return true
		}
	}
	return false
}
