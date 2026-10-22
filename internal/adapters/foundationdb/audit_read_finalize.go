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

// auditReadReceipt is the terminal record of one read. PublishedAt is zero
// until the relay delivers the event.
type auditReadReceipt struct {
	Owner       uuid.UUID     `json:"owner"`
	Generation  uint64        `json:"generation"`
	Digest      string        `json:"digest"`
	Outcome     audit.Outcome `json:"outcome"`
	PublishedAt time.Time     `json:"published_at,omitzero" exhaustruct:"optional"`
}

// auditRecord lists the JSON records of the read lifecycle families.
type auditRecord interface {
	auditReadPending | auditReadReceipt
}

// encodedOutboxWriter writes an encoded event to the outbox inside tr.
type encodedOutboxWriter interface {
	writeEncoded(tr fdb.Transaction, encoded audit.EncodedEvent) error
}

// versionstampOutboxWriter writes each outbox record of an encoded event as
// one versionstamped outbox entry, parts first and the manifest last. A
// single-record event writes its plain event JSON.
type versionstampOutboxWriter struct{}

func (versionstampOutboxWriter) writeEncoded(tr fdb.Transaction, encoded audit.EncodedEvent) error {
	messages, err := encoded.Records()
	if err != nil {
		return auditReadStepFailure("encode outbox messages", err)
	}
	var userVersion uint16
	for _, message := range messages {
		key, err := tuple.Tuple{keyOpsOutbox, tuple.IncompleteVersionstamp(userVersion)}.PackWithVersionstamp(testPrefix)
		if err != nil {
			return auditReadStepFailure("pack outbox message key", err)
		}
		tr.SetVersionstampedKey(fdb.Key(key), message)
		userVersion++
	}
	return nil
}

func auditReadReceiptKey(eventID uuid.UUID) fdb.Key {
	return fdb.Key(withPrefix(tuple.Tuple{keyAuditReadReceipt, eventID.String()}.Pack()))
}

func auditReadCleanupKey(bucket uint16, published time.Time, eventID uuid.UUID) fdb.Key {
	return fdb.Key(withPrefix(tuple.Tuple{keyAuditReadCleanup, int64(bucket), published.UnixNano(), eventID.String()}.Pack()))
}

// FinalizeRead saves the final event in the outbox and its digest in a
// terminal receipt, and removes the pending read, in one transaction. When
// the pending read is absent, a receipt from the same owner with the same
// digest confirms an earlier commit. Finalization never recreates pending
// state.
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
		if err := s.outbox.writeEncoded(tr, encoded); err != nil {
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

// confirmFinalized accepts a retry only when the receipt confirms the same
// owner, digest, and a terminal outcome other than incomplete.
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

// AcknowledgePublished marks the receipts of delivered reads published and
// schedules their cleanup in one transaction.
func (s *AuditReadStore) AcknowledgePublished(ctx context.Context, reads []audit.PublishedRead) error {
	err := transactAuditRead(ctx, s.db, func(tr fdb.Transaction) error {
		return acknowledgeReads(tr, s.clock.Now(), reads)
	})
	if err != nil {
		return auditReadFailure(ctx, "acknowledge audit reads", uuid.Nil, err)
	}
	return nil
}

// acknowledgeReads marks each matching receipt published at now inside tr.
// It skips a read without a receipt and a receipt already published. A
// digest that differs from the receipt refuses the transaction.
func acknowledgeReads(tr fdb.Transaction, now time.Time, reads []audit.PublishedRead) error {
	for _, read := range reads {
		key := auditReadReceiptKey(read.EventID)
		receipt, found, err := readAuditRecord[auditReadReceipt](tr, key)
		if err != nil {
			return err
		}
		if !found || !receipt.PublishedAt.IsZero() {
			continue
		}
		if receipt.Digest != read.Digest {
			return fmt.Errorf("published digest of audit read %s differs from its receipt", read.EventID)
		}
		receipt.PublishedAt = now
		if err := writeAuditRecord(tr, key, receipt); err != nil {
			return err
		}
		tr.Set(auditReadCleanupKey(audit.ReadBucket(read.EventID), now, read.EventID), nil)
	}
	return nil
}

// readAuditRecord decodes the JSON record at key and reports false when the
// key is absent.
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
