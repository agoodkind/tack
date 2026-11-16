package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"goodkind.io/tack/internal/telemetry"
)

// Redactor implements GDPR Right to Erasure for the audit ledger. It uses
// tack_audit_redactor, which inherits audit_redactor and can UPDATE only
// payload, redacted, and redacted_at in audit.pii. The role can SELECT from
// audit.pii but cannot access other audit tables.
// The hash chain covers pii_ref, not the erased payload.
type Redactor struct {
	pool *pgxpool.Pool
}

// NewRedactor opens a pool against the audit_redactor DSN.
func NewRedactor(ctx context.Context, dsn string) (*Redactor, error) {
	if dsn == "" {
		return nil, errors.New("audit: AUDIT_REDACTOR_DSN required")
	}

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("audit redactor pool config: %w", err)
	}

	cfg.ConnConfig.Tracer = &telemetry.QueryTracer{}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("audit redactor pool open: %w", err)
	}

	// The pool connects on first use after a failed startup ping.
	if err := pool.Ping(ctx); err != nil {
		slog.WarnContext(ctx, "audit.redactor.ping_deferred", slog.String("err", err.Error()))
	}

	return &Redactor{pool: pool}, nil
}

func (r *Redactor) Close() {
	if r != nil && r.pool != nil {
		r.pool.Close()
	}
}

// CountUnredacted counts the given audit.pii rows that retain their payloads.
// The redactor role can SELECT from audit.pii without reading audit.events.
func (r *Redactor) CountUnredacted(ctx context.Context, refs []uuid.UUID) (int64, error) {
	if r == nil || r.pool == nil {
		return 0, errors.New("audit redactor not configured")
	}

	if len(refs) == 0 {
		return 0, nil
	}

	var count int64
	err := r.pool.QueryRow(
		ctx,
		`
		SELECT count(*) FROM audit.pii
		 WHERE pii_ref = ANY($1::uuid[]) AND redacted = false
	`,
		uuidStrings(refs),
	).Scan(
		&count,
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit.pii.count_unredacted_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("audit count unredacted pii: %w", err)
	}

	return count, nil
}

// RedactPIIRefs erases the payloads of the given audit.pii rows and returns
// the number of changed rows. The caller resolves references through the
// reader within one org. The redactor does not read audit.events.
func (r *Redactor) RedactPIIRefs(ctx context.Context, refs []uuid.UUID) (int64, error) {
	ctx, span := telemetry.StartSpan(
		ctx,
		"audit.redact_pii_refs",
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attribute.Int("audit.pii_ref_count", len(refs))),
	)
	defer span.End()
	ctx = telemetry.WithTraceLogger(ctx, slog.Int("pii_ref_count", len(refs)))
	if r == nil || r.pool == nil {
		return 0, errors.New("audit redactor not configured")
	}

	if len(refs) == 0 {
		return 0, nil
	}

	tag, err := r.pool.Exec(
		ctx,
		`
		UPDATE audit.pii
		   SET payload = NULL, redacted = true, redacted_at = now()
		 WHERE pii_ref = ANY($1::uuid[]) AND redacted = false
	`,
		uuidStrings(refs),
	)
	if err != nil {
		slog.ErrorContext(ctx, "audit.pii.redact_refs_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("audit redact pii refs: %w", err)
	}

	count := tag.RowsAffected()
	telemetry.L(ctx).Info(
		"audit.pii.redacted",
		slog.Int("pii_ref_count", len(refs)),
		slog.Int64("redacted_count", count),
	)

	return count, nil
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}

	return out
}
