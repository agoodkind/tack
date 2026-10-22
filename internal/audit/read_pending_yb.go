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
	// readOwnerLostSQLState is the error code of a lifecycle function that
	// refuses a token.
	readOwnerLostSQLState = "TK001"
	// readAdmissionExpiredSQLState is the error code of an expired admission retry.
	readAdmissionExpiredSQLState = "TK002"
)

// PoolReadLifecycle implements ReadLifecycle for operator reads through the
// lifecycle functions of migration 021. The functions use database time.
type PoolReadLifecycle struct {
	pool *pgxpool.Pool
}

// NewPoolReadLifecycle returns the YugabyteDB read lifecycle over pool.
func NewPoolReadLifecycle(pool *pgxpool.Pool) *PoolReadLifecycle {
	return &PoolReadLifecycle{pool: pool}
}

var _ ReadLifecycle = (*PoolReadLifecycle)(nil)

// readStatement returns the embedded SQL statement with name.
func readStatement(name string) string {
	statement, err := readStatements.ReadFile("read_sql/" + name + ".sql")
	if err != nil {
		slog.Error("audit.read_lifecycle.statement_missing", slog.String("name", name), slog.String("err", err.Error()))
		return ""
	}
	return string(statement)
}

// BeginRead saves the pending read through ops_read_begin.
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

// RenewRead extends the lease through ops_read_renew.
func (l *PoolReadLifecycle) RenewRead(ctx context.Context, token ReadToken) error {
	_, err := l.pool.Exec(ctx, readStatement("renew"), token.EventID, token.Owner, token.Generation)
	if err != nil {
		return readLifecycleFailure(ctx, "renew audit read", token.EventID, err)
	}
	return nil
}

// FinalizeRead reads the owned template, checks the final event against it,
// and saves the event and receipt through ops_read_finalize. A NULL template
// means that the receipt confirms an earlier finalization.
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

// AcknowledgePublished marks each delivered receipt published in one
// transaction through ops_read_acknowledge.
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

// splitOutboxRecords returns the plain event or manifest record of encoded
// and its ordered part records, the arguments of ops_outbox_write_encoded.
func splitOutboxRecords(encoded EncodedEvent) ([]byte, [][]byte, error) {
	records, err := encoded.Records()
	if err != nil {
		return nil, nil, err
	}
	last := len(records) - 1
	return records[last], records[:last], nil
}

// readLifecycleFailure logs and wraps one failed lifecycle call. It adds
// ErrReadOwnerLost or ErrReadAdmissionExpired for the matching function error.
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
