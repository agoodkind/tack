package audit

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed read_sql/*.sql
var readStatements embed.FS

const (
	// readOwnerLostSQLState is the error code for a lifecycle function that
	// rejects a token.
	readOwnerLostSQLState = "TK001"
	// readAdmissionExpiredSQLState is the error code of an expired admission retry.
	readAdmissionExpiredSQLState = "TK002"
)

// PoolReadLifecycle stores pending operator reads and terminal receipts in YugabyteDB.
// Its lifecycle functions use database time.
type PoolReadLifecycle struct {
	pool *pgxpool.Pool
}

// NewPoolReadLifecycle returns a YugabyteDB read lifecycle that uses pool.
func NewPoolReadLifecycle(pool *pgxpool.Pool) *PoolReadLifecycle {
	return &PoolReadLifecycle{pool: pool}
}

var _ ReadLifecycle = (*PoolReadLifecycle)(nil)

// readStatement returns the embedded SQL statement identified by name.
func readStatement(name string) string {
	statement, err := readStatements.ReadFile("read_sql/" + name + ".sql")
	if err != nil {
		slog.Error("audit.read_lifecycle.statement_missing", slog.String("name", name), slog.String("err", err.Error()))
		return ""
	}
	return string(statement)
}

// BeginRead saves pending state before the caller reads data.
func (l *PoolReadLifecycle) BeginRead(ctx context.Context, admission ReadAdmission) (ReadToken, error) {
	token := admission.Token
	if err := ValidateReadAdmission(admission); err != nil {
		return ReadToken{}, readLifecycleFailure(ctx, "begin audit read", token.EventID, err)
	}
	template, err := json.Marshal(admission.Template)
	if err != nil {
		return ReadToken{}, readLifecycleFailure(ctx, "encode audit read template", token.EventID, err)
	}
	var generation uint64
	var deadline time.Time
	bucket := int(ReadBucket(token.EventID))
	err = l.pool.QueryRow(ctx, readStatement("begin"), token.EventID, token.Owner, bucket, template).
		Scan(&generation, &deadline)
	if err != nil {
		return ReadToken{}, readLifecycleFailure(ctx, "begin audit read", token.EventID, err)
	}
	return ReadToken{EventID: token.EventID, Owner: token.Owner, Generation: generation, AdmissionDeadline: deadline}, nil
}

// RenewRead extends the lease only when token still owns the pending read.
func (l *PoolReadLifecycle) RenewRead(ctx context.Context, token ReadToken) error {
	_, err := l.pool.Exec(ctx, readStatement("renew"), token.EventID, token.Owner, token.Generation)
	if err != nil {
		return readLifecycleFailure(ctx, "renew audit read", token.EventID, err)
	}
	return nil
}

// FinalizeRead checks the final event against the pending template and saves
// the event in the outbox with a terminal receipt before the caller returns data.
// On retry, a NULL template confirms an earlier commit for the same owner and digest.
// FinalizeRead never recreates missing pending state.
func (l *PoolReadLifecycle) FinalizeRead(ctx context.Context, completion ReadCompletion) error {
	token := completion.Token
	generation := token.Generation
	var stored []byte
	err := l.pool.QueryRow(ctx, readStatement("template"), token.EventID, token.Owner, generation, completion.Digest).Scan(&stored)
	if err != nil {
		return readLifecycleFailure(ctx, "read audit read template", token.EventID, err)
	}
	if stored == nil {
		return nil
	}
	var template Event
	if err := json.Unmarshal(stored, &template); err != nil {
		return readLifecycleFailure(ctx, "decode audit read template", token.EventID, err)
	}
	encoded, err := CheckReadCompletion(template, completion)
	if err != nil {
		return readLifecycleFailure(ctx, "finalize audit read", token.EventID, err)
	}
	header, parts, err := splitOutboxRecords(encoded)
	if err != nil {
		return readLifecycleFailure(ctx, "encode audit read event", token.EventID, err)
	}
	_, err = l.pool.Exec(ctx, readStatement("finalize"), token.EventID, token.Owner, generation,
		encoded.TransportDigest, string(completion.Event.Outcome), header, parts)
	if err != nil {
		return readLifecycleFailure(ctx, "finalize audit read", token.EventID, err)
	}
	return nil
}

// AcknowledgePublished marks the supplied read receipts as published in one transaction.
func (l *PoolReadLifecycle) AcknowledgePublished(ctx context.Context, reads []PublishedRead) error {
	err := pgx.BeginFunc(ctx, l.pool, func(tx pgx.Tx) error {
		for _, read := range reads {
			if _, err := tx.Exec(ctx, readStatement("acknowledge"), read.EventID, read.Digest); err != nil {
				return readLifecycleFailure(ctx, "acknowledge audit read", read.EventID, err)
			}
		}
		return nil
	})
	if err != nil {
		return readLifecycleFailure(ctx, "acknowledge audit reads", uuid.Nil, err)
	}
	return nil
}

// splitOutboxRecords separates the plain event or manifest from the ordered parts
// for ops_outbox_write_encoded.
func splitOutboxRecords(encoded EncodedEvent) ([]byte, [][]byte, error) {
	records, err := encoded.Records()
	if err != nil {
		return nil, nil, err
	}
	last := len(records) - 1
	return records[last], records[:last], nil
}

// readLifecycleFailure logs a lifecycle error and adds the operation and event ID.
// It includes ErrReadOwnerLost or ErrReadAdmissionExpired when the SQL error code matches.
func readLifecycleFailure(ctx context.Context, operation string, eventID uuid.UUID, err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case readOwnerLostSQLState:
			err = errors.Join(ErrReadOwnerLost, err)
		case readAdmissionExpiredSQLState:
			err = errors.Join(ErrReadAdmissionExpired, err)
		}
	}
	wrapped := fmt.Errorf("%s %s: %w", operation, eventID, err)
	slog.ErrorContext(ctx, "audit.read_lifecycle.failed",
		slog.String("operation", operation), slog.String("event_id", eventID.String()), slog.String("err", wrapped.Error()))
	return wrapped
}
