package foundationdb

import "github.com/apple/foundationdb/bindings/go/src/fdb/tuple"

func strictActivationKey() []byte {
	return withPrefix(tuple.Tuple{keyStrictActivation}.Pack())
}

func writeFenceBucketKey(bucket int) []byte {
	return withPrefix(tuple.Tuple{keyWriteFence, int64(bucket)}.Pack())
}
