package foundationdb

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/telemetry"
)

// versionsPerSecond is FoundationDB's nominal version rate for converting
// lease durations to read versions.
const versionsPerSecond = 1_000_000

// ErrRelayLeaseLost reports an attempt to clear outbox entries without
// owning the relay lease.
var ErrRelayLeaseLost = errors.New("relay does not own the FoundationDB outbox lease")

// relayLeaseRecord stores the relay owner ID and the FoundationDB read
// version at which the lease expires.
type relayLeaseRecord struct {
	Owner          uuid.UUID `json:"owner"`
	ExpiresVersion int64     `json:"expires_version"`
}

// LeasedOpsOutbox checks relay ownership in each transaction that clears
// operator outbox entries.
type LeasedOpsOutbox struct {
	store    *OpsOutboxStore
	owner    uuid.UUID
	versions int64
}

// Leased creates a relay outbox with owner and duration.
//
// The returned outbox uses those values to acquire and renew the lease.
//
//   - duration converts to a FoundationDB version interval.
func (s *OpsOutboxStore) Leased(owner uuid.UUID, duration time.Duration) *LeasedOpsOutbox {
	return &LeasedOpsOutbox{store: s, owner: owner, versions: int64(duration.Seconds() * versionsPerSecond)}
}

func opsOutboxRelayLeaseKey() fdb.Key {
	return fdb.Key(withPrefix(tuple.Tuple{keyOpsOutboxRelayLease}.Pack()))
}

// Hold acquires or renews the caller's relay lease.
//
// It uses the transaction's FoundationDB read version to check expiration
// and calculate the new expiration version.
//
//   - It returns false when another relay owns an unexpired lease.
func (l *LeasedOpsOutbox) Hold(ctx context.Context) (held bool, err error) {
	defer telemetry.FDBOp(ctx, "store.ops_outbox.relay_lease_hold")(&err)
	err = transactAuditRead(ctx, l.store.db, func(tr fdb.Transaction) error {
		held = false
		readVersion, versionErr := tr.GetReadVersion().Get()
		if versionErr != nil {
			return auditReadStepFailure("read the relay lease version", versionErr)
		}
		lease, found, readErr := readRelayLease(tr)
		if readErr != nil {
			return readErr
		}
		if found && lease.Owner != l.owner && readVersion < lease.ExpiresVersion {
			return nil
		}
		held = true
		return writeRelayLease(tr, relayLeaseRecord{Owner: l.owner, ExpiresVersion: readVersion + l.versions})
	})
	if err != nil {
		return false, auditReadFailure(ctx, "hold the ops outbox relay lease", l.owner, err)
	}
	return held, nil
}

// Release deletes the relay lease when the caller still owns it.
//
// It checks the current owner and deletes the lease in one transaction.
//
//   - It does not delete a missing lease or a lease with a different owner.
func (l *LeasedOpsOutbox) Release(ctx context.Context) (err error) {
	defer telemetry.FDBOp(ctx, "store.ops_outbox.relay_lease_release")(&err)
	err = transactAuditRead(ctx, l.store.db, func(tr fdb.Transaction) error {
		lease, found, readErr := readRelayLease(tr)
		if readErr != nil || !found || lease.Owner != l.owner {
			return readErr
		}
		tr.Clear(opsOutboxRelayLeaseKey())
		return nil
	})
	if err != nil {
		return auditReadFailure(ctx, "release the ops outbox relay lease", l.owner, err)
	}
	return nil
}

// ReadOutboxFrom reads up to limit entries after mark in commit order.
//
// It delegates to the store without checking the relay lease.
//
//   - A nil mark starts at the beginning of the outbox.
func (l *LeasedOpsOutbox) ReadOutboxFrom(ctx context.Context, mark []byte, limit int) ([]OutboxEntry, error) {
	return l.store.ReadOutboxFrom(ctx, mark, limit)
}

// ClearThrough deletes entries up to and including mark under the relay
// lease.
//
// It delegates to ClearThroughPublished without read receipts.
//
//   - A nonempty mark requires a lease that the caller owns. An absent lease
//     or a different owner causes an error that wraps ErrRelayLeaseLost.
func (l *LeasedOpsOutbox) ClearThrough(ctx context.Context, mark []byte) error {
	return l.ClearThroughPublished(ctx, mark, nil)
}

// ClearThroughPublished clears entries and acknowledges matching read
// receipts in one transaction under the relay lease.
//
// It checks ownership before clearing entries up to and including mark. A
// takeover after the lease read forces the transaction to retry and check
// the new owner.
//
//   - A nonempty mark requires a lease that the caller owns. Otherwise,
//     ErrRelayLeaseLost prevents the clear.
//   - An empty mark returns without checking the lease.
func (l *LeasedOpsOutbox) ClearThroughPublished(ctx context.Context, mark []byte, reads []audit.PublishedRead) (err error) {
	defer telemetry.FDBOp(ctx, "store.ops_outbox.leased_clear_through")(&err)
	if len(mark) == 0 {
		return nil
	}
	clearRange, err := outboxClearRange(ctx, mark)
	if err != nil {
		return err
	}
	err = transactAuditRead(ctx, l.store.db, func(tr fdb.Transaction) error {
		lease, found, readErr := readRelayLease(tr)
		if readErr != nil {
			return readErr
		}
		if !found || lease.Owner != l.owner {
			return ErrRelayLeaseLost
		}
		tr.ClearRange(clearRange)
		return acknowledgeReads(tr, processClock{}.Now(), reads)
	})
	if err != nil {
		return auditReadFailure(ctx, "clear delivered outbox entries under the relay lease", l.owner, err)
	}
	return nil
}

// readRelayLease reads and decodes the lease in tr and reports whether the
// lease exists.
func readRelayLease(tr fdb.Transaction) (relayLeaseRecord, bool, error) {
	var lease relayLeaseRecord
	encoded, err := tr.Get(opsOutboxRelayLeaseKey()).Get()
	if err != nil {
		return lease, false, auditReadStepFailure("read the relay lease", err)
	}
	if len(encoded) == 0 {
		return lease, false, nil
	}
	if err := json.Unmarshal(encoded, &lease); err != nil {
		return lease, false, auditReadStepFailure("decode the relay lease", err)
	}
	return lease, true, nil
}

// writeRelayLease encodes lease as JSON and writes it in tr.
func writeRelayLease(tr fdb.Transaction, lease relayLeaseRecord) error {
	encoded, err := json.Marshal(lease)
	if err != nil {
		return auditReadStepFailure("encode the relay lease", err)
	}
	tr.Set(opsOutboxRelayLeaseKey(), encoded)
	return nil
}
