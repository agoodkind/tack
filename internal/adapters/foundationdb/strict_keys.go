package foundationdb

import "github.com/apple/foundationdb/bindings/go/src/fdb/tuple"

// The strict validation keys store the global activation record and write
// fence bucket epochs. Neither key family uses an organization scope.
// (strict_activation) -> StrictActivation JSON
// (write_fence, bucket) -> little-endian int64 epoch that atomic adds increment
const (
	keyStrictActivation = "strict_activation"
	keyWriteFence       = "write_fence"
)

func strictActivationKey() []byte {
	return withPrefix(tuple.Tuple{keyStrictActivation}.Pack())
}

func writeFenceBucketKey(bucket int) []byte {
	return withPrefix(tuple.Tuple{keyWriteFence, int64(bucket)}.Pack())
}
