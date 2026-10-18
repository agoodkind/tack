package foundationdb

import "bytes"

// testPrefix is the prefix that withPrefix prepends to every packed FDB key
// when non-nil. Production code leaves it nil.
//
// Tests in one test binary share an FDB cluster and run one after another.
// Each test uses SetTestPrefix to set its own UUID prefix. Its keys stay in
// a range that other tests in that binary do not write.
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
func SetTestPrefix(prefix []byte) {
	testPrefix = prefix
}

// withPrefix prepends the active test prefix (if any) to a packed key. The
// production fast path returns the input unchanged.
func withPrefix(key []byte) []byte {
	if testPrefix == nil {
		return key
	}
	out := make([]byte, 0, len(testPrefix)+len(key))
	out = append(out, testPrefix...)
	out = append(out, key...)
	return out
}

// stripPrefix removes the active test prefix from a key returned by an FDB
// range read. Production callers leave testPrefix nil and the function is a
// no-op. Stores must call this before tuple.Unpack on a returned key so the
// unpack starts at the real tuple bytes rather than the raw test prefix.
func stripPrefix(key []byte) []byte {
	if testPrefix == nil {
		return key
	}
	if len(key) >= len(testPrefix) && bytes.Equal(key[:len(testPrefix)], testPrefix) {
		return key[len(testPrefix):]
	}
	return key
}

// TestPrefixRange returns a range covering every key under the active test
// prefix. Tests use this to range-clear all data they wrote during a run.
// Returns nil when no test prefix is set, so production code is safe to call.
func TestPrefixRange() []byte {
	if testPrefix == nil {
		return nil
	}
	return testPrefix
}
