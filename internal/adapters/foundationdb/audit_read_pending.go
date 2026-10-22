package foundationdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// auditReadTransactionTimeout limits one read lifecycle transaction.
const auditReadTransactionTimeout = 5 * time.Second

// AuditReadStore implements [audit.ReadLifecycle] for product reads.
type AuditReadStore struct {
	db     fdb.Database
	clock  clock.Clock
	outbox encodedOutboxWriter
}

// AuditReads returns the read lifecycle store on the shared connection. The
// clock decides admission, lease, and retention times.
func (s *Stores) AuditReads(source clock.Clock) *AuditReadStore {
	return &AuditReadStore{db: s.db, clock: source, outbox: versionstampOutboxWriter{}}
}

var _ audit.ReadLifecycle = (*AuditReadStore)(nil)

// auditReadPending is one admitted read that has not saved a terminal event.
type auditReadPending struct {
	Owner             uuid.UUID       `json:"owner"`
	Generation        uint64          `json:"generation"`
	AdmissionDeadline time.Time       `json:"admission_deadline"`
	LeaseDeadline     time.Time       `json:"lease_deadline"`
	Template          json.RawMessage `json:"template"`
}

func auditReadPendingKey(eventID uuid.UUID) fdb.Key {
	return fdb.Key(withPrefix(tuple.Tuple{keyAuditReadPending, eventID.String()}.Pack()))
}

func auditReadExpiryKey(bucket uint16, deadline time.Time, eventID uuid.UUID) fdb.Key {
	return fdb.Key(withPrefix(tuple.Tuple{keyAuditReadExpiry, int64(bucket), deadline.UnixNano(), eventID.String()}.Pack()))
}

// auditReadDueRange covers the expiry or cleanup keys of bucket with a time
// at or before limit.
func auditReadDueRange(family string, bucket uint16, limit time.Time) fdb.KeyRange {
	begin := withPrefix(tuple.Tuple{family, int64(bucket)}.Pack())
	end := withPrefix(tuple.Tuple{family, int64(bucket), limit.UnixNano() + 1}.Pack())
	return fdb.KeyRange{Begin: fdb.Key(begin), End: fdb.Key(end)}
}

// BeginRead saves the pending read with generation one, an admission
// deadline, and a lease. A retry of the same admission by the same owner
// before its deadline returns the saved token.
func (s *AuditReadStore) BeginRead(ctx context.Context, admission audit.ReadAdmission) (audit.ReadToken, error) {
	eventID := admission.Token.EventID
	if err := audit.ValidateReadAdmission(admission); err != nil {
		return audit.ReadToken{}, auditReadFailure(ctx, "begin audit read", eventID, err)
	}
	template, err := json.Marshal(admission.Template)
	if err != nil {
		return audit.ReadToken{}, auditReadFailure(ctx, "encode audit read template", eventID, err)
	}
	var token audit.ReadToken
	err = transactAuditRead(ctx, s.db, func(tr fdb.Transaction) error {
		now := s.clock.Now()
		pending, found, err := readAuditRecord[auditReadPending](tr, auditReadPendingKey(eventID))
		if err != nil {
			return err
		}
		if found {
			token, err = retriedAdmission(pending, admission.Token.Owner, template, now)
			token.EventID = eventID
			return err
		}
		_, finished, err := readAuditRecord[auditReadReceipt](tr, auditReadReceiptKey(eventID))
		if err != nil {
			return err
		}
		if finished {
			return audit.ErrReadOwnerLost
		}
		pending = auditReadPending{
			Owner: admission.Token.Owner, Generation: audit.ReadFirstGeneration,
			AdmissionDeadline: now.Add(audit.ReadAdmissionRetryWindow), LeaseDeadline: now.Add(audit.ReadLease),
			Template: template,
		}
		token = audit.ReadToken{
			EventID: eventID, Owner: pending.Owner, Generation: pending.Generation,
			AdmissionDeadline: pending.AdmissionDeadline,
		}
		return writePending(tr, eventID, pending)
	})
	if err != nil {
		return audit.ReadToken{}, auditReadFailure(ctx, "begin audit read", eventID, err)
	}
	return token, nil
}

// retriedAdmission returns the saved token for a retry by the same owner
// with the same template before the admission deadline.
func retriedAdmission(pending auditReadPending, owner uuid.UUID, template []byte, now time.Time) (audit.ReadToken, error) {
	if pending.Owner != owner || !bytes.Equal(pending.Template, template) {
		return audit.ReadToken{}, audit.ErrReadOwnerLost
	}
	if now.After(pending.AdmissionDeadline) {
		return audit.ReadToken{}, audit.ErrReadAdmissionExpired
	}
	return audit.ReadToken{
		EventID: uuid.Nil, Owner: pending.Owner, Generation: pending.Generation,
		AdmissionDeadline: pending.AdmissionDeadline,
	}, nil
}

// RenewRead replaces the lease deadline and its expiry key when the token
// owns the pending read.
func (s *AuditReadStore) RenewRead(ctx context.Context, token audit.ReadToken) error {
	err := transactAuditRead(ctx, s.db, func(tr fdb.Transaction) error {
		pending, err := ownedPending(tr, token)
		if err != nil {
			return err
		}
		tr.Clear(auditReadExpiryKey(audit.ReadBucket(token.EventID), pending.LeaseDeadline, token.EventID))
		pending.LeaseDeadline = s.clock.Now().Add(audit.ReadLease)
		return writePending(tr, token.EventID, pending)
	})
	if err != nil {
		return auditReadFailure(ctx, "renew audit read", token.EventID, err)
	}
	return nil
}

// ownedPending reads the pending read and requires the token's owner and
// generation.
func ownedPending(tr fdb.Transaction, token audit.ReadToken) (auditReadPending, error) {
	pending, found, err := readAuditRecord[auditReadPending](tr, auditReadPendingKey(token.EventID))
	if err != nil {
		return pending, err
	}
	if !found || pending.Owner != token.Owner || pending.Generation != token.Generation {
		return pending, audit.ErrReadOwnerLost
	}
	return pending, nil
}

// writePending saves the pending record and its expiry key.
func writePending(tr fdb.Transaction, eventID uuid.UUID, pending auditReadPending) error {
	if err := writeAuditRecord(tr, auditReadPendingKey(eventID), pending); err != nil {
		return err
	}
	tr.Set(auditReadExpiryKey(audit.ReadBucket(eventID), pending.LeaseDeadline, eventID), nil)
	return nil
}

// clearPending removes the pending record and its expiry key.
func clearPending(tr fdb.Transaction, eventID uuid.UUID, pending auditReadPending) {
	tr.Clear(auditReadPendingKey(eventID))
	tr.Clear(auditReadExpiryKey(audit.ReadBucket(eventID), pending.LeaseDeadline, eventID))
}

// auditReadFailure logs and wraps one failed lifecycle operation.
func auditReadFailure(ctx context.Context, operation string, eventID uuid.UUID, err error) error {
	wrapped := fmt.Errorf("%s %s: %w", operation, eventID, err)
	slog.ErrorContext(ctx, "audit.read_lifecycle.failed",
		slog.String("operation", operation), slog.String("event_id", eventID.String()), slog.String("err", wrapped.Error()))
	return wrapped
}
