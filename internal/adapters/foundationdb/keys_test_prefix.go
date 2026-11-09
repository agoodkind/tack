package foundationdb

import "bytes"

// testPrefix supplies the bytes that withPrefix prepends to packed
// FoundationDB keys when testPrefix is non-nil. Production code leaves it
// nil.
//
// Tests in one test binary share a FoundationDB cluster and run one after
// another. Each test uses SetTestPrefix to set its own UUID prefix. Other
// tests in that binary do not write that key range.
//
// testPrefix is a package variable without a lock. A test that sets it must
// not run in parallel with another test in the same binary. Only
// SetTestPrefix assigns it.
var testPrefix []byte

// SetTestPrefix sets the per-test key prefix. A nil argument clears it.
//
// withPrefix prepends the prefix as raw bytes before the packed tuple key.
// The prefix is not a tuple element. Stores call stripPrefix on each key
// that a range read returns before calling tuple.Unpack.
//
// SetTestPrefix is for test code only. Production code must not invoke it.
//
//	SetTestPrefix(prefix)
//	defer SetTestPrefix(nil)
func SetTestPrefix(prefix []byte) {
	testPrefix = prefix
}

// withPrefix prepends the active test prefix to a packed key. When
// testPrefix is nil, it returns the input unchanged.
func withPrefix(key []byte) []byte {
	if testPrefix == nil {
		return key
	}
	out := make([]byte, 0, len(testPrefix)+len(key))
	out = append(out, testPrefix...)
	out = append(out, key...)
	return out
}

// stripPrefix removes the active test prefix from a key that a FoundationDB
// range read returns. Production callers leave testPrefix nil, and
// stripPrefix returns the input unchanged. Stores must call stripPrefix
// before tuple.Unpack on a returned key. Unpacking must start at the tuple
// bytes after the raw test prefix.
func stripPrefix(key []byte) []byte {
	if testPrefix == nil {
		return key
	}
	if len(key) >= len(testPrefix) && bytes.Equal(key[:len(testPrefix)], testPrefix) {
		return key[len(testPrefix):]
	}
	return key
}

// TestPrefixRange returns the active test prefix.
//
// Tests use this prefix to clear the keys they wrote during a run. It
// returns nil when the test prefix does not exist, including in production.
// Production calls do not change any keys.
//
//	SetTestPrefix(prefix)
//	active := TestPrefixRange() // active contains prefix.
//	SetTestPrefix(nil)
func TestPrefixRange() []byte {
	if testPrefix == nil {
		return nil
	}
	return testPrefix
}
