package foundationdb

import "bytes"

// testPrefix is prepended to every packed FDB key when non-nil. Production
// callers leave it nil; integration tests set a per-test UUID via
// SetTestPrefix so parallel tests on a shared FDB cluster cannot collide.
//
// Set only via SetTestPrefix from the test setup helper.
var testPrefix []byte

// SetTestPrefix configures a per-test key prefix. Pass nil to clear. The
// prefix is encoded as a single bytes element in the tuple layer so range
// scans stay tuple-aware.
//
// Tests own this. Do not call from production code.
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
