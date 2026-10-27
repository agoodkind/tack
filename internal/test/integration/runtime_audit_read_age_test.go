package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
)

const (
	// receiptAge moves a published receipt one minute past the retention cutoff.
	receiptAge = audit.ReadReceiptRetention + time.Minute
	// receiptAgeTimeout limits the FoundationDB transaction that ages a receipt.
	receiptAgeTimeout = 5 * time.Second
	// The receipt and cleanup key families match the read lifecycle keys in
	// internal/adapters/foundationdb/keys.go.
	receiptKeyFamily = "audit_read_receipt"
	cleanupKeyFamily = "audit_read_cleanup"
)

// ageFoundationDBReceipt requires the receipt of eventID under prefix to have a
// publication time and the matching cleanup key, which the relay writes in the
// transaction that clears the delivered outbox entries. It then moves the
// publication time and the cleanup key back by receiptAge.
func ageFoundationDBReceipt(t *testing.T, clusterFile string, prefix []byte, eventID uuid.UUID) {
	t.Helper()
	database, err := fdbadapter.Open(clusterFile, receiptAgeTimeout)
	if err != nil {
		t.Fatalf("open FoundationDB to age the receipt of read %s: %v", eventID, err)
	}
	receiptKey := prefixedKey(prefix, tuple.Tuple{receiptKeyFamily, eventID.String()})
	bucket := int64(audit.ReadBucket(eventID))
	_, err = database.Transact(func(tr fdb.Transaction) (any, error) {
		encoded, err := tr.Get(receiptKey).Get()
		if err != nil {
			return nil, fmt.Errorf("read the receipt: %w", err)
		}
		var receipt map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &receipt); err != nil {
			return nil, fmt.Errorf("decode the receipt %q: %w", encoded, err)
		}
		var published time.Time
		if err := json.Unmarshal(receipt["published_at"], &published); err != nil || published.IsZero() {
			return nil, fmt.Errorf("the receipt %s does not have a publication time", encoded)
		}
		cleanupKey := prefixedKey(prefix, tuple.Tuple{cleanupKeyFamily, bucket, published.UnixNano(), eventID.String()})
		marker, err := tr.GetRange(fdb.KeyRange{Begin: cleanupKey, End: fdb.Key(append(bytes.Clone(cleanupKey), 0x00))}, fdb.RangeOptions{Limit: 1}).GetSliceWithError()
		if err != nil {
			return nil, fmt.Errorf("read the cleanup key: %w", err)
		}
		if len(marker) != 1 {
			return nil, fmt.Errorf("the receipt published at %s does not have a cleanup key", published)
		}
		aged := published.Add(-receiptAge)
		if receipt["published_at"], err = json.Marshal(aged); err != nil {
			return nil, fmt.Errorf("encode the aged publication time: %w", err)
		}
		rewritten, err := json.Marshal(receipt)
		if err != nil {
			return nil, fmt.Errorf("encode the aged receipt: %w", err)
		}
		tr.Set(receiptKey, rewritten)
		tr.Clear(cleanupKey)
		tr.Set(prefixedKey(prefix, tuple.Tuple{cleanupKeyFamily, bucket, aged.UnixNano(), eventID.String()}), nil)
		return nil, nil
	})
	if err != nil {
		t.Fatalf("age the FoundationDB receipt of read %s: %v", eventID, err)
	}
}

// prefixedKey returns the packed key under the raw test prefix.
func prefixedKey(prefix []byte, key tuple.Tuple) fdb.Key {
	return fdb.Key(append(bytes.Clone(prefix), key.Pack()...))
}
