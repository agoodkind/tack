package foundationdb

import (
	"bytes"
	"context"
	"slices"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// ClaimBucketChoices limits how many of the oldest pending buckets a claim
// rotates among. Multiple choices let concurrent claims start in different
// buckets.
const ClaimBucketChoices = 4

// pendingBucket records a class bucket with age keys and the part of its
// first age key after the bucket prefix. That part sorts by enqueue time.
type pendingBucket struct {
	bucket int
	oldest []byte
}

// claimOrder sorts pending buckets by first age key, oldest first. It
// rotates up to ClaimBucketChoices oldest buckets by rotation modulo their
// count, then appends the remaining buckets oldest first.
func claimOrder(pending []pendingBucket, rotation int64) []int {
	sorted := slices.Clone(pending)
	slices.SortFunc(sorted, func(first, second pendingBucket) int {
		return bytes.Compare(first.oldest, second.oldest)
	})
	choices := min(ClaimBucketChoices, len(sorted))
	start := int(rotation % int64(choices))
	if start < 0 {
		start += choices
	}
	order := make([]int, 0, len(sorted))
	for offset := range choices {
		order = append(order, sorted[(start+offset)%choices].bucket)
	}
	for _, candidate := range sorted[choices:] {
		order = append(order, candidate.bucket)
	}
	return order
}

// classHasAgeKey reports whether class has at least one age key in any
// bucket. It reads at most one key with one snapshot range read limited to
// one key.
func classHasAgeKey(ctx context.Context, tr fdb.Transaction, class searchdomain.WorkClass) (bool, error) {
	keyRange, err := fdb.PrefixRange(searchAgeClassPrefix(string(class)))
	if err != nil {
		return false, searchReadFailure(ctx, "create search age range of class "+string(class), err)
	}
	first, err := tr.Snapshot().GetRange(keyRange, fdb.RangeOptions{Limit: 1}).GetSliceWithError()
	if err != nil {
		return false, searchReadFailure(ctx, "probe search age keys of class "+string(class), err)
	}
	return len(first) > 0, nil
}
