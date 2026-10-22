package foundationdb

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// auditReadReceipt stores the terminal record of one read. PublishedAt remains
// zero until the relay acknowledges publication.
type auditReadReceipt struct {
	Owner       uuid.UUID     `json:"owner"`
	Generation  uint64        `json:"generation"`
	Digest      string        `json:"digest"`
	Outcome     audit.Outcome `json:"outcome"`
	PublishedAt time.Time     `json:"published_at,omitzero" exhaustruct:"optional"`
}

// auditRecord restricts read lifecycle JSON records to pending reads and terminal receipts.
type auditRecord interface {
	auditReadPending | auditReadReceipt
}

func auditReadReceiptKey(eventID uuid.UUID) fdb.Key {
	return fdb.Key(withPrefix(tuple.Tuple{keyAuditReadReceipt, eventID.String()}.Pack()))
}

func auditReadCleanupKey(bucket uint16, published time.Time, eventID uuid.UUID) fdb.Key {
	return fdb.Key(withPrefix(tuple.Tuple{keyAuditReadCleanup, int64(bucket), published.UnixNano(), eventID.String()}.Pack()))
}

// FinalizeRead saves the final event in the outbox, saves its digest in a terminal
// receipt, and removes pending state in one transaction. If pending state does not
// exist, a receipt from the same owner with the same digest confirms an earlier
// commit. Finalization never recreates missing pending state.
func (s *AuditReadStore) FinalizeRead(ctx context.Context, completion audit.ReadCompletion) error {
	token := completion.Token
	err := transactAuditRead(ctx, s.db, func(tr fdb.Transaction) error {
		pending, found, err := readAuditRecord[auditReadPending](tr, auditReadPendingKey(token.EventID))
		if err != nil {
			return err
		}
		if !found {
			return confirmFinalized(tr, completion)
		}
		if pending.Owner != token.Owner || pending.Generation != token.Generation {
			return audit.ErrReadOwnerLost
		}
		var template audit.Event
		if err := json.Unmarshal(pending.Template, &template); err != nil {
			return auditReadStepFailure("decode the pending template", err)
		}
		encoded, err := audit.CheckReadCompletion(template, completion)
		if err != nil {
			return auditReadStepFailure("check the final event", err)
		}
		if err := NewOutboxWriter(tr).WriteEncodedEvent(ctx, encoded); err != nil {
			return err
		}
		clearPending(tr, token.EventID, pending)
		return writeAuditRecord(tr, auditReadReceiptKey(token.EventID), auditReadReceipt{
			Owner: token.Owner, Generation: token.Generation, Digest: encoded.TransportDigest,
			Outcome: completion.Event.Outcome,
		})
	})
	if err != nil {
		return auditReadFailure(ctx, "finalize audit read", token.EventID, err)
	}
	return nil
}

// confirmFinalized accepts a retry only when the receipt matches the owner and
// digest and records a terminal outcome other than incomplete.
func confirmFinalized(tr fdb.Transaction, completion audit.ReadCompletion) error {
	receipt, found, err := readAuditRecord[auditReadReceipt](tr, auditReadReceiptKey(completion.Token.EventID))
	if err != nil {
		return err
	}
	if !found || !audit.ConfirmsReadCommit(receipt.Owner, receipt.Digest, receipt.Outcome, completion) {
		return audit.ErrReadOwnerLost
	}
	return nil
}

// AcknowledgePublished marks matching receipts as published and schedules their
// cleanup in one transaction.
func (s *AuditReadStore) AcknowledgePublished(ctx context.Context, reads []audit.PublishedRead) error {
	err := transactAuditRead(ctx, s.db, func(tr fdb.Transaction) error {
		return acknowledgeReads(tr, s.clock.Now(), reads)
	})
	if err != nil {
		return auditReadFailure(ctx, "acknowledge audit reads", uuid.Nil, err)
	}
	return nil
}

// acknowledgeReads skips reads without receipts. It returns an error when the
// acknowledgment digest differs from the receipt digest, even after publication.
// It does not change a published receipt when the digest matches. For an unpublished
// receipt with a matching digest, it records now as the publication time and schedules
// cleanup within tr.
func acknowledgeReads(tr fdb.Transaction, now time.Time, reads []audit.PublishedRead) error {
	for _, read := range reads {
		key := auditReadReceiptKey(read.EventID)
		receipt, found, err := readAuditRecord[auditReadReceipt](tr, key)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if receipt.Digest != read.Digest {
			return fmt.Errorf("acknowledge audit read %s: digest differs from the receipt digest", read.EventID)
		}
		if !receipt.PublishedAt.IsZero() {
			continue
		}
		receipt.PublishedAt = now
		if err := writeAuditRecord(tr, key, receipt); err != nil {
			return err
		}
		tr.Set(auditReadCleanupKey(audit.ReadBucket(read.EventID), now, read.EventID), nil)
	}
	return nil
}

// readAuditRecord decodes the JSON record at key and returns false when the
// key does not exist.
func readAuditRecord[Record auditRecord](tr fdb.Transaction, key fdb.Key) (Record, bool, error) {
	var record Record
	encoded, err := tr.Get(key).Get()
	if err != nil {
		return record, false, auditReadStepFailure("read audit read record", err)
	}
	if len(encoded) == 0 {
		return record, false, nil
	}
	if err := json.Unmarshal(encoded, &record); err != nil {
		return record, false, auditReadStepFailure("decode audit read record", err)
	}
	return record, true, nil
}

// writeAuditRecord encodes record as JSON at key.
func writeAuditRecord[Record auditRecord](tr fdb.Transaction, key fdb.Key, record Record) error {
	encoded, err := json.Marshal(record)
	if err != nil {
		return auditReadStepFailure("encode audit read record", err)
	}
	tr.Set(key, encoded)
	return nil
}
